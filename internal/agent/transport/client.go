package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"servermonitor/internal/agent/spool"
	"servermonitor/pkg/wire"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	logger  *slog.Logger
	spool   *spool.Spool

	drainOnce        sync.Once
	deregisteredOnce sync.Once
	deregisteredCh   chan struct{}

	intervalCh       chan int
	appliedMu        sync.Mutex
	appliedIntervalS int

	controlCh chan ControlUpdate
}

type ControlUpdate struct {
	LatestVersion string
	AutoUpgrade   *bool
	UpgradeNow    bool
}

var ErrDeregistered = errors.New("host deregistered by server")

func New(baseURL, token string, timeout time.Duration, insecureSkip bool, logger *slog.Logger, sp *spool.Spool) *Client {
	t := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecureSkip},
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &Client{
		baseURL:        baseURL,
		token:          token,
		http:           &http.Client{Timeout: timeout, Transport: t},
		logger:         logger,
		spool:          sp,
		deregisteredCh: make(chan struct{}),
		intervalCh:     make(chan int, 1),
		controlCh:      make(chan ControlUpdate, 1),
	}
}

func (c *Client) ControlUpdates() <-chan ControlUpdate {
	return c.controlCh
}

func (c *Client) IntervalUpdates() <-chan int {
	return c.intervalCh
}

func (c *Client) SetCurrentInterval(s int) {
	c.appliedMu.Lock()
	c.appliedIntervalS = s
	c.appliedMu.Unlock()
}

func (c *Client) Deregistered() <-chan struct{} {
	return c.deregisteredCh
}

func (c *Client) markDeregistered() {
	c.deregisteredOnce.Do(func() { close(c.deregisteredCh) })
}

func (c *Client) Send(ctx context.Context, batch *wire.Batch) error {
	body, err := gzipJSON(batch)
	if err != nil {
		return err
	}
	if err := c.postBytes(ctx, body); err != nil {
		if !isRetryable(err) {
			c.logger.Warn("ingest rejected (permanent), dropping batch", "err", err, "points", len(batch.Points))
			return err
		}
		if c.spool == nil {
			return err
		}
		c.logger.Warn("ingest failed, spooling to disk", "err", err, "points", len(batch.Points))
		if spErr := c.spool.Enqueue(body); spErr != nil {
			return fmt.Errorf("send failed and spool failed: send=%v spool=%v", err, spErr)
		}
		return nil
	}
	return nil
}

func (c *Client) StartDrainer(ctx context.Context) {
	if c.spool == nil {
		return
	}
	c.drainOnce.Do(func() {
		go c.drainLoop(ctx)
	})
}

func (c *Client) drainLoop(ctx context.Context) {
	wait := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.deregisteredCh:
			return
		case <-time.After(wait):
		}
		key, body, err := c.spool.Peek()
		if err != nil {
			wait = 30 * time.Second
			continue
		}
		if err := c.postBytes(ctx, body); err != nil {
			if errors.Is(err, ErrDeregistered) {
				return
			}
			if !isRetryable(err) {
				c.logger.Warn("spool entry rejected (permanent), dropping", "err", err)
				if delErr := c.spool.Delete(key); delErr != nil {
					c.logger.Warn("spool delete failed", "err", delErr)
				}
				wait = 250 * time.Millisecond
				continue
			}
			c.logger.Debug("spool drain still failing", "err", err)
			wait = nextBackoff(wait)
			continue
		}
		if err := c.spool.Delete(key); err != nil {
			c.logger.Warn("spool delete failed", "err", err)
		}
		wait = 250 * time.Millisecond
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

func (c *Client) postBytes(ctx context.Context, body []byte) error {
	select {
	case <-c.deregisteredCh:
		return ErrDeregistered
	default:
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-Agent-Token", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		ackBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_, _ = io.Copy(io.Discard, resp.Body)
		var ack struct {
			IntervalS          int    `json:"interval_s"`
			LatestAgentVersion string `json:"latest_agent_version"`
			AutoUpgrade        *bool  `json:"auto_upgrade"`
			UpgradeNow         bool   `json:"upgrade_now"`
		}
		if len(ackBytes) > 0 {
			_ = json.Unmarshal(ackBytes, &ack)
		}
		if ack.IntervalS > 0 {
			c.appliedMu.Lock()
			cur := c.appliedIntervalS
			if ack.IntervalS != cur {
				c.appliedIntervalS = ack.IntervalS
			}
			c.appliedMu.Unlock()
			if ack.IntervalS != cur {
				select {
				case c.intervalCh <- ack.IntervalS:
				default:
				}
			}
		}
		if ack.LatestAgentVersion != "" || ack.UpgradeNow || ack.AutoUpgrade != nil {
			upd := ControlUpdate{
				LatestVersion: ack.LatestAgentVersion,
				AutoUpgrade:   ack.AutoUpgrade,
				UpgradeNow:    ack.UpgradeNow,
			}
			select {
			case c.controlCh <- upd:
			default:
				select {
				case <-c.controlCh:
				default:
				}
				select {
				case c.controlCh <- upd:
				default:
				}
			}
		}
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusGone {
		c.markDeregistered()
		return ErrDeregistered
	}
	return &httpError{status: resp.StatusCode, body: string(data)}
}

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("status %d: %s", e.status, e.body)
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrDeregistered) {
		return false
	}
	var he *httpError
	if errors.As(err, &he) {
		switch he.status {
		case http.StatusRequestTimeout, http.StatusTooManyRequests:
			return true
		}
		if he.status >= 500 && he.status <= 599 {
			return true
		}
		return false
	}
	return true
}

func gzipJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if err := json.NewEncoder(gz).Encode(v); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
