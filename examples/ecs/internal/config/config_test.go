package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsMalformedEnvironmentInsteadOfUsingDefaults(t *testing.T) {
	t.Setenv("AGENT_MYSQL_DSN", "user:password@tcp(mysql:3306)/agent")
	t.Setenv("AGENT_WORKER_POLL_INTERVAL", "quickly")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AGENT_WORKER_POLL_INTERVAL") {
		t.Fatalf("error=%v", err)
	}
}

func TestLoadRejectsLeaseTooShortForHeartbeat(t *testing.T) {
	t.Setenv("AGENT_MYSQL_DSN", "user:password@tcp(mysql:3306)/agent")
	t.Setenv("AGENT_WORKER_POLL_INTERVAL", "5s")
	t.Setenv("AGENT_WORKER_LEASE_DURATION", "10s")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "at least three polling intervals") {
		t.Fatalf("error=%v", err)
	}
}
