//go:build linux

package ipban

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const fleetChunk = 1000

type nftEnforcer struct {
	conn   *nftables.Conn
	table  *nftables.Table
	chain  *nftables.Chain
	local4 *nftables.Set
	local6 *nftables.Set
	fleet4 *nftables.Set
	fleet6 *nftables.Set
}

func newTable() *nftables.Table {
	return &nftables.Table{Family: nftables.TableFamilyINet, Name: TableName}
}

func newSet(table *nftables.Table, name string) *nftables.Set {
	keyType := nftables.TypeIPAddr
	if strings.HasSuffix(name, "6") {
		keyType = nftables.TypeIP6Addr
	}
	return &nftables.Set{Table: table, Name: name, KeyType: keyType, HasTimeout: true}
}

func NewEnforcer() (Enforcer, error) {
	conn, err := nftables.New(nftables.AsLasting())
	if err != nil {
		return nil, err
	}
	table := newTable()
	policy := nftables.ChainPolicyAccept
	e := &nftEnforcer{
		conn:  conn,
		table: table,
		chain: &nftables.Chain{
			Name:     ChainName,
			Table:    table,
			Type:     nftables.ChainTypeFilter,
			Hooknum:  nftables.ChainHookPrerouting,
			Priority: nftables.ChainPriorityRaw,
			Policy:   &policy,
		},
		local4: newSet(table, "local4"),
		local6: newSet(table, "local6"),
		fleet4: newSet(table, "fleet4"),
		fleet6: newSet(table, "fleet6"),
	}
	return e, nil
}

func (e *nftEnforcer) sets() []*nftables.Set {
	return []*nftables.Set{e.local4, e.local6, e.fleet4, e.fleet6}
}

func dropRule(table *nftables.Table, chain *nftables.Chain, set *nftables.Set) *nftables.Rule {
	proto := byte(unix.NFPROTO_IPV4)
	offset, length := uint32(12), uint32(4)
	if set.KeyType.Name == nftables.TypeIP6Addr.Name {
		proto = unix.NFPROTO_IPV6
		offset, length = 8, 16
	}
	return &nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: length},
			&expr.Lookup{SourceRegister: 1, SetName: set.Name, SetID: set.ID},
			&expr.Verdict{Kind: expr.VerdictDrop},
		},
	}
}

func (e *nftEnforcer) Setup() error {
	e.conn.AddTable(e.table)
	e.conn.AddChain(e.chain)
	for _, s := range e.sets() {
		if err := e.conn.AddSet(s, nil); err != nil {
			return err
		}
	}
	e.conn.FlushChain(e.chain)
	for _, s := range e.sets() {
		e.conn.AddRule(dropRule(e.table, e.chain, s))
	}
	return e.conn.Flush()
}

func element(ip netip.Addr, timeout time.Duration) nftables.SetElement {
	if timeout < minElementTTL {
		timeout = minElementTTL
	}
	ip = ip.Unmap()
	if ip.Is4() {
		key := ip.As4()
		return nftables.SetElement{Key: key[:], Timeout: timeout}
	}
	key := ip.As16()
	return nftables.SetElement{Key: key[:], Timeout: timeout}
}

func (e *nftEnforcer) localSet(ip netip.Addr) *nftables.Set {
	if ip.Unmap().Is4() {
		return e.local4
	}
	return e.local6
}

func (e *nftEnforcer) AddLocal(ip netip.Addr, timeout time.Duration) error {
	set := e.localSet(ip)
	el := element(ip, timeout)
	exists, err := setContains(e.conn, set, ip)
	if err != nil {
		return err
	}
	if exists {
		if err := e.conn.SetDeleteElements(set, []nftables.SetElement{{Key: el.Key}}); err != nil {
			return err
		}
	}
	if err := e.conn.SetAddElements(set, []nftables.SetElement{el}); err != nil {
		return err
	}
	return e.conn.Flush()
}

func setContains(conn *nftables.Conn, set *nftables.Set, want netip.Addr) (bool, error) {
	elements, err := conn.GetSetElements(set)
	if err != nil {
		return false, err
	}
	want = want.Unmap()
	for _, el := range elements {
		ip, ok := netip.AddrFromSlice(el.Key)
		if ok && ip.Unmap() == want {
			return true, nil
		}
	}
	return false, nil
}

func (e *nftEnforcer) RemoveLocal(ip netip.Addr) error {
	set := e.localSet(ip)
	el := element(ip, minElementTTL)
	if err := e.conn.SetDeleteElements(set, []nftables.SetElement{{Key: el.Key}}); err != nil {
		return err
	}
	err := e.conn.Flush()
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (e *nftEnforcer) FlushLocal() error {
	e.conn.FlushSet(e.local4)
	e.conn.FlushSet(e.local6)
	return e.conn.Flush()
}

func (e *nftEnforcer) ReplaceFleet(entries []FleetEntry) error {
	e.conn.FlushSet(e.fleet4)
	e.conn.FlushSet(e.fleet6)
	var v4, v6 []nftables.SetElement
	for _, entry := range entries {
		ip := entry.IP.Unmap()
		if ip.Is4() {
			v4 = append(v4, element(ip, entry.Timeout))
		} else {
			v6 = append(v6, element(ip, entry.Timeout))
		}
	}
	if err := e.addChunked(e.fleet4, v4); err != nil {
		return err
	}
	if err := e.addChunked(e.fleet6, v6); err != nil {
		return err
	}
	return e.conn.Flush()
}

func (e *nftEnforcer) addChunked(set *nftables.Set, elements []nftables.SetElement) error {
	for len(elements) > 0 {
		n := len(elements)
		if n > fleetChunk {
			n = fleetChunk
		}
		if err := e.conn.SetAddElements(set, elements[:n]); err != nil {
			return err
		}
		elements = elements[n:]
	}
	return nil
}

func (e *nftEnforcer) Active() ([]ActiveEntry, []ActiveEntry, error) {
	var local, fleet []ActiveEntry
	for _, s := range e.sets() {
		entries, err := readSet(e.conn, s)
		if err != nil {
			return nil, nil, err
		}
		if strings.HasPrefix(s.Name, "local") {
			local = append(local, entries...)
		} else {
			fleet = append(fleet, entries...)
		}
	}
	return local, fleet, nil
}

func readSet(conn *nftables.Conn, set *nftables.Set) ([]ActiveEntry, error) {
	elements, err := conn.GetSetElements(set)
	if err != nil {
		return nil, err
	}
	out := make([]ActiveEntry, 0, len(elements))
	for _, el := range elements {
		ip, ok := netip.AddrFromSlice(el.Key)
		if !ok {
			continue
		}
		out = append(out, ActiveEntry{IP: ip.Unmap(), Expires: el.Expires})
	}
	return out, nil
}

func (e *nftEnforcer) Close() error {
	return e.conn.CloseLasting()
}

func Teardown() error {
	conn, err := nftables.New()
	if err != nil {
		return err
	}
	conn.DelTable(newTable())
	err = conn.Flush()
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func tablePresent(conn *nftables.Conn) (bool, error) {
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return false, err
	}
	for _, t := range tables {
		if t.Name == TableName {
			return true, nil
		}
	}
	return false, nil
}

func Snapshot() ([]SetSnapshot, error) {
	conn, err := nftables.New()
	if err != nil {
		return nil, err
	}
	present, err := tablePresent(conn)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	table := newTable()
	out := make([]SetSnapshot, 0, len(setNames))
	for _, name := range setNames {
		entries, err := readSet(conn, newSet(table, name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, SetSnapshot{Name: name, Entries: entries})
	}
	return out, nil
}

func isUnsupportedErrno(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EPROTONOSUPPORT) || errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.ENOSYS)
}

func HasNetAdmin() (bool, bool) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "CapEff" {
			continue
		}
		bits, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			return false, false
		}
		return bits&(1<<unix.CAP_NET_ADMIN) != 0, true
	}
	return false, false
}
