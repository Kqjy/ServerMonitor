package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type Notification struct {
	Kind      string            `json:"kind"`
	RuleID    int32             `json:"rule_id"`
	RuleName  string            `json:"rule_name"`
	HostID    int64             `json:"host_id"`
	Hostname  string            `json:"hostname"`
	Metric    string            `json:"metric"`
	Unit      string            `json:"unit,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64           `json:"value"`
	Threshold float64           `json:"threshold"`
	Cmp       string            `json:"comparator"`
	Severity  string            `json:"severity"`
	FiredAt   time.Time         `json:"fired_at"`
}

type Dispatcher struct {
	logger *slog.Logger
	http   *http.Client
}

func NewDispatcher(logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		logger: logger,
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (d *Dispatcher) Send(ctx context.Context, kind, name string, cfg []byte, n Notification) {
	go func() {
		var err error
		switch kind {
		case "smtp":
			err = d.sendSMTP(ctx, cfg, n)
		case "webhook":
			err = d.sendWebhook(ctx, cfg, n)
		default:
			err = fmt.Errorf("unknown channel kind: %s", kind)
		}
		if err != nil {
			d.logger.Warn("notify failed", "channel", name, "kind", kind, "err", err)
		}
	}()
}

type smtpConfig struct {
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	Username   string   `json:"username"`
	Password   string   `json:"password"`
	From       string   `json:"from"`
	To         []string `json:"to"`
	StartTLS   bool     `json:"starttls"`
}

func (d *Dispatcher) sendSMTP(ctx context.Context, cfg []byte, n Notification) error {
	var c smtpConfig
	if err := json.Unmarshal(cfg, &c); err != nil {
		return err
	}
	if c.Host == "" || c.From == "" || len(c.To) == 0 {
		return errors.New("smtp config requires host, from, to")
	}
	if c.Port == 0 {
		c.Port = 587
	}

	subject := smtpSubject(n)
	body := smtpBody(n)
	msg := []byte("From: " + c.From + "\r\n" +
		"To: " + strings.Join(c.To, ", ") + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" + body)

	addr := c.Host + ":" + strconv.Itoa(c.Port)
	var auth smtp.Auth
	if c.Username != "" {
		auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
	}

	return retry(ctx, 3, func() error {
		return smtp.SendMail(addr, auth, c.From, c.To, msg)
	})
}

func smtpSubject(n Notification) string {
	prefix := strings.ToUpper(n.Severity)
	if n.Kind == "resolve" {
		prefix = "RESOLVED"
	}
	return fmt.Sprintf("[%s] %s on %s", prefix, n.RuleName, n.Hostname)
}

func smtpBody(n Notification) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Rule:      %s\n", n.RuleName)
	fmt.Fprintf(&sb, "Host:      %s\n", n.Hostname)
	fmt.Fprintf(&sb, "Metric:    %s\n", n.Metric)
	if len(n.Labels) > 0 {
		fmt.Fprintf(&sb, "Labels:    %v\n", n.Labels)
	}
	fmt.Fprintf(&sb, "Value:     %.4f %s\n", n.Value, n.Unit)
	fmt.Fprintf(&sb, "Threshold: %s %.4f\n", n.Cmp, n.Threshold)
	fmt.Fprintf(&sb, "Severity:  %s\n", n.Severity)
	fmt.Fprintf(&sb, "When:      %s\n", n.FiredAt.Format(time.RFC3339))
	return sb.String()
}

type webhookConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Format  string            `json:"format,omitempty"`
}

func (d *Dispatcher) sendWebhook(ctx context.Context, cfg []byte, n Notification) error {
	var c webhookConfig
	if err := json.Unmarshal(cfg, &c); err != nil {
		return err
	}
	if c.URL == "" {
		return errors.New("webhook config requires url")
	}
	method := strings.ToUpper(c.Method)
	if method == "" {
		method = http.MethodPost
	}

	var body []byte
	contentType := "application/json"
	switch strings.ToLower(c.Format) {
	case "discord":
		body = discordBody(n)
	case "slack":
		body = slackBody(n)
	case "ntfy":
		body = []byte(ntfyBody(n))
		contentType = "text/plain"
	default:
		body, _ = json.Marshal(n)
	}

	return retry(ctx, 3, func() error {
		req, err := http.NewRequestWithContext(ctx, method, c.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("User-Agent", "ServerMonitor/0.1")
		for k, v := range c.Headers {
			req.Header.Set(k, v)
		}
		resp, err := d.http.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(data))
	})
}

func discordBody(n Notification) []byte {
	color := 0xB45309
	if n.Kind == "resolve" {
		color = 0x10B981
	} else if n.Severity == "critical" {
		color = 0xE11D48
	}
	body := map[string]any{
		"embeds": []map[string]any{{
			"title":       smtpSubject(n),
			"description": smtpBody(n),
			"color":       color,
		}},
	}
	b, _ := json.Marshal(body)
	return b
}

func slackBody(n Notification) []byte {
	body := map[string]any{
		"text": fmt.Sprintf("*%s* — %s on `%s`\n```%s```", strings.ToUpper(n.Severity), n.RuleName, n.Hostname, smtpBody(n)),
	}
	b, _ := json.Marshal(body)
	return b
}

func ntfyBody(n Notification) string {
	return smtpSubject(n) + "\n\n" + smtpBody(n)
}

func retry(ctx context.Context, attempts int, fn func() error) error {
	var err error
	delay := 500 * time.Millisecond
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return err
}
