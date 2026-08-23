package cmd

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestRunDockerBackupCreate_HappyPath(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/backup" {
			t.Errorf("path = %s, want /api/backup", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"polaris-20260101-030000.db","path":"/data/backups/polaris-20260101-030000.db","size_bytes":2097152,"created_at":"2026-01-01T03:00:00Z"}`))
	})

	output := captureStdout(t, func() {
		if err := runDockerBackupCreate(); err != nil {
			t.Fatalf("runDockerBackupCreate() error = %v, want nil", err)
		}
	})

	if !strings.Contains(output, "polaris-20260101-030000.db") {
		t.Errorf("output = %q, want the backup name", output)
	}
	if !strings.Contains(output, "2.0MiB") {
		t.Errorf("output = %q, want the human-readable size", output)
	}
}

func TestRunDockerBackupList_HappyPath(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/backup" {
			t.Errorf("path = %s, want /api/backup", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"polaris-20260102-030000.db","path":"/data/backups/polaris-20260102-030000.db","size_bytes":1024,"created_at":"2026-01-02T03:00:00Z"}]`))
	})

	output := captureStdout(t, func() {
		if err := runDockerBackupList(); err != nil {
			t.Fatalf("runDockerBackupList() error = %v, want nil", err)
		}
	})

	if !strings.Contains(output, "polaris-20260102-030000.db") {
		t.Errorf("output = %q, want the backup name", output)
	}
}

func TestRunDockerBackupList_EmptyPrintsFriendlyMessage(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	output := captureStdout(t, func() {
		if err := runDockerBackupList(); err != nil {
			t.Fatalf("runDockerBackupList() error = %v, want nil", err)
		}
	})

	if !strings.Contains(output, "no backups yet") {
		t.Errorf("output = %q, want a friendly empty-state message", output)
	}
}

func TestRunDockerBackupCreate_ServerError(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("disk full"))
	})

	if err := runDockerBackupCreate(); err == nil {
		t.Fatal("runDockerBackupCreate() error = nil, want an error on a 500")
	}
}

func TestDockerRestoreInstructions_NamesTheBackupAndTheFullSequence(t *testing.T) {
	out := dockerRestoreInstructions("polaris-20260101-030000.db")
	for _, want := range []string{
		"docker compose stop polaris",
		"docker compose run --rm --no-deps polaris backup restore polaris-20260101-030000.db",
		"docker compose up -d polaris",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("instructions = %q, want it to contain %q", out, want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1.0KiB"},
		{1536, "1.5KiB"},
		{1024 * 1024 * 2, "2.0MiB"},
	}
	for _, c := range cases {
		if got := humanSize(c.in); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestServerAppearsRunning_NothingListening(t *testing.T) {
	// Grab a real free port, then close it immediately — nothing should
	// be listening there for the actual check.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(lis.Addr().String())
	lis.Close()
	port, _ := strconv.Atoi(portStr)

	if serverAppearsRunning(port) {
		t.Error("serverAppearsRunning() = true, want false when nothing is listening on the port")
	}
}

func TestServerAppearsRunning_HealthzResponding(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go srv.Serve(lis)
	t.Cleanup(func() { srv.Close() })

	_, portStr, _ := net.SplitHostPort(lis.Addr().String())
	port, _ := strconv.Atoi(portStr)

	if !serverAppearsRunning(port) {
		t.Error("serverAppearsRunning() = false, want true when something answers 200 on the port")
	}
}
