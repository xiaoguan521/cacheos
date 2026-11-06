package instance

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xiaoguan521/cacheos/internal/agent/resp"
)

func TestReadReplicationSnapshot(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		br := bufio.NewReader(server)
		if _, err := br.ReadString('\n'); err != nil { // array header
			return
		}
		for i := 0; i < 2; i++ {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "$")))
			if _, err := io.CopyN(io.Discard, br, int64(n+2)); err != nil {
				return
			}
		}
		body := "# Replication\r\nrole:slave\r\nmaster_host:10.0.0.8\r\nmaster_port:6379\r\nmaster_link_status:up\r\nmaster_sync_in_progress:0\r\nmaster_repl_offset:120\r\nslave_repl_offset:118\r\ncluster_enabled:0\r\n"
		_, _ = fmt.Fprintf(server, "$%d\r\n%s\r\n", len(body), body)
	}()

	s, err := readReplicationSnapshot(context.Background(), resp.New(client))
	if err != nil {
		t.Fatal(err)
	}
	if !isReplicaRole(s.Role) || s.MasterHost != "10.0.0.8" || s.MasterPort != 6379 || s.MasterLinkStatus != "up" || s.SourceOffset != 120 || s.ReplicaOffset != 118 || s.ClusterEnabled {
		t.Fatalf("unexpected replication snapshot: %+v", s)
	}
}

func TestSourceMatchesSupportsBracketedIPv6(t *testing.T) {
	info := replicationSnapshot{MasterHost: "2001:db8::8", MasterPort: 6379}
	if !sourceMatches(info, "[2001:db8::8]:6379") {
		t.Fatal("expected IPv6 source to match")
	}
	if sourceMatches(info, "10.0.0.8:6379") {
		t.Fatal("unexpected source match")
	}
}

func TestWaitNativeReplicationRequiresStableSourceOffset(t *testing.T) {
	target, closeTarget := fakeInfoConnection(t, []string{
		"role:slave\r\nmaster_link_status:down\r\nmaster_sync_in_progress:1\r\nmaster_repl_offset:0\r\nslave_repl_offset:0\r\n",
		"role:slave\r\nmaster_link_status:up\r\nmaster_sync_in_progress:0\r\nmaster_repl_offset:9\r\nslave_repl_offset:9\r\n",
		"role:slave\r\nmaster_link_status:up\r\nmaster_sync_in_progress:0\r\nmaster_repl_offset:10\r\nslave_repl_offset:10\r\n",
	})
	source, closeSource := fakeInfoConnection(t, []string{
		"role:master\r\nmaster_repl_offset:10\r\n",
	})
	defer closeTarget()
	defer closeSource()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := (&Manager{}).waitNativeReplication(ctx, target, source, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceOffset != 10 || got.ReplicaOffset != 10 || got.MasterLinkStatus != "up" {
		t.Fatalf("unexpected caught-up snapshot: %+v", got)
	}
}

func fakeInfoConnection(t *testing.T, bodies []string) (*resp.Conn, func()) {
	t.Helper()
	server, client := net.Pipe()
	go func() {
		br := bufio.NewReader(server)
		i := 0
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if !strings.HasPrefix(line, "*") {
				return
			}
			count, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
			for j := 0; j < count; j++ {
				line, err = br.ReadString('\n')
				if err != nil {
					return
				}
				n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "$")))
				if _, err := io.CopyN(io.Discard, br, int64(n+2)); err != nil {
					return
				}
			}
			body := bodies[minInt(i, len(bodies)-1)]
			full := "# Replication\r\n" + body
			if _, err := fmt.Fprintf(server, "$%d\r\n%s\r\n", len(full), full); err != nil {
				return
			}
			i++
		}
	}()
	return resp.New(client), func() {
		_ = client.Close()
		_ = server.Close()
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
