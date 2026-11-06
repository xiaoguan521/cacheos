package instance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/resp"
)

var migratePatternRe = regexp.MustCompile(`^(?:\*|[A-Za-z0-9:_.\/-]+\*)$`)

type migrateReport struct {
	JobID           string `json:"job_id"`
	Mode            string `json:"mode"`
	Strategy        string `json:"strategy"`
	SourceAddress   string `json:"source_address"`
	Pattern         string `json:"pattern"`
	Passes          int    `json:"passes"`
	KeysScanned     uint64 `json:"keys_scanned"`
	KeysCopied      uint64 `json:"keys_copied"`
	KeysDeleted     uint64 `json:"keys_deleted"`
	KeysSkipped     uint64 `json:"keys_skipped"`
	KeysFailed      uint64 `json:"keys_failed"`
	SourceQuiesced  bool   `json:"source_quiesced"`
	CutoverReady    bool   `json:"cutover_ready"`
	AwaitingCutover bool   `json:"awaiting_cutover"`
	FullSyncDone    bool   `json:"full_sync_done"`
	SourceOffset    int64  `json:"source_repl_offset,omitempty"`
	TargetOffset    int64  `json:"target_repl_offset,omitempty"`
	DurationSeconds int64  `json:"duration_seconds"`
	LastError       string `json:"last_error,omitempty"`
}

type replicationSnapshot struct {
	Role             string
	MasterHost       string
	MasterPort       uint32
	MasterLinkStatus string
	SyncInProgress   bool
	SourceOffset     int64
	ReplicaOffset    int64
	ClusterEnabled   bool
}

func validateMigrationSource(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("source_address must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("source_address has an invalid port")
	}
	if strings.ContainsAny(address, "\r\n\"'\\") {
		return fmt.Errorf("source_address contains forbidden characters")
	}
	return nil
}

// Migrate copies keys from an arbitrary RESP-compatible source to one
// local managed instance. FULL uses SCAN/RESTORE for compatibility with
// sources that do not expose replication. FULL_INCREMENTAL uses the target's
// native replication handshake, so the source RDB and subsequent command
// stream travel directly between the data-plane processes; values never pass
// through the control plane. The target can remain a replica while the
// operator prepares the cutover, then finalize=true promotes it safely.
func (m *Manager) Migrate(ctx context.Context, req *agentv1.Migrate) ([]byte, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, err
	}
	if err := validateMigrationSource(req.GetSourceAddress()); err != nil {
		return nil, errf("INVALID", "%v", err)
	}
	pattern := strings.TrimSpace(req.GetPattern())
	if pattern == "" {
		pattern = "*"
	}
	if !migratePatternRe.MatchString(pattern) {
		return nil, errf("INVALID", "pattern must be * or a literal prefix ending in *")
	}
	batch := req.GetBatch()
	if batch == 0 || batch > 1000 {
		batch = 200
	}
	passes := uint32(1)
	mode := req.GetMode()
	if mode == agentv1.Migrate_MODE_UNSPECIFIED {
		mode = agentv1.Migrate_FULL
	}
	if mode != agentv1.Migrate_FULL && mode != agentv1.Migrate_FULL_INCREMENTAL {
		return nil, errf("INVALID", "unsupported migration mode %s", mode.String())
	}

	target, err := m.Dial(ctx, st)
	if err != nil {
		return nil, errf("INTERNAL", "connect target: %v", err)
	}
	defer target.Close()
	source, err := resp.DialTCP(ctx, req.GetSourceAddress(), req.GetSourceUser(), req.GetSourcePassword())
	if err != nil {
		return nil, errf("INTERNAL", "connect source: %v", err)
	}
	defer source.Close()

	if req.GetFinalize() {
		return m.finalizeNativeMigration(ctx, st, req, target, source)
	}
	if mode == agentv1.Migrate_FULL_INCREMENTAL {
		if pattern != "*" {
			return nil, errf("UNSUPPORTED", "native incremental migration copies the whole database and cannot filter by pattern")
		}
		return m.migrateByReplication(ctx, st, req, target, source)
	}

	started := time.Now()
	report := migrateReport{JobID: req.GetJobId(), Mode: mode.String(), Strategy: "scan", SourceAddress: req.GetSourceAddress(), Pattern: pattern, SourceQuiesced: req.GetSourceQuiesced()}
	for pass := uint32(0); pass < passes; pass++ {
		report.Passes++
		cursor := "0"
		for {
			v, err := source.Do(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(int(batch)))
			if err != nil {
				return nil, errf("INTERNAL", "source scan: %v", err)
			}
			if len(v.Array) < 2 {
				return nil, errf("INTERNAL", "invalid source SCAN response")
			}
			cursor = v.Array[0].Text()
			for _, key := range v.Array[1].Strings() {
				report.KeysScanned++
				ttl, err := source.Do(ctx, "PTTL", key)
				if err != nil || ttl.Int == -2 {
					report.KeysSkipped++
					continue
				}
				dump, err := source.Do(ctx, "DUMP", key)
				if err != nil || dump.Null {
					report.KeysSkipped++
					continue
				}
				expire := ttl.Int
				if expire < 0 {
					expire = 0
				}
				if _, err := target.Do(ctx, "RESTORE", key, strconv.FormatInt(expire, 10), dump.Str, "REPLACE"); err != nil {
					report.KeysFailed++
					report.LastError = err.Error()
					continue
				}
				report.KeysCopied++
			}
			if cursor == "0" {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}
	}
	report.DurationSeconds = int64(time.Since(started) / time.Second)
	report.FullSyncDone = true
	report.CutoverReady = report.KeysFailed == 0
	b, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func readReplicationSnapshot(ctx context.Context, c *resp.Conn) (replicationSnapshot, error) {
	v, err := c.Do(ctx, "INFO", "replication")
	if err != nil {
		return replicationSnapshot{}, err
	}
	i := resp.ParseInfo(v.Text())
	s := replicationSnapshot{
		Role:             i["role"],
		MasterHost:       i["master_host"],
		MasterPort:       uint32(atoi(i["master_port"])),
		MasterLinkStatus: i["master_link_status"],
		SyncInProgress:   i["master_sync_in_progress"] == "1",
		SourceOffset:     atoi(i["master_repl_offset"]),
		ReplicaOffset:    atoi(i["slave_repl_offset"]),
		ClusterEnabled:   i["cluster_enabled"] == "1",
	}
	// Some older Redis-compatible servers do not expose slave_repl_offset;
	// their master_repl_offset is the last applied offset on a replica.
	if s.ReplicaOffset == 0 && (s.Role == "slave" || s.Role == "replica") {
		s.ReplicaOffset = s.SourceOffset
	}
	return s, nil
}

func isReplicaRole(role string) bool { return role == "slave" || role == "replica" }

func (m *Manager) refreshManifest(st *State) error {
	conf, err := os.ReadFile(m.L.ConfPath(st.Port, st.Kind))
	if err != nil {
		return err
	}
	return m.writeManifest(st, conf)
}

func (m *Manager) configureNativeReplication(ctx context.Context, st *State, req *agentv1.Migrate, target *resp.Conn, host string, port int) error {
	// CONFIG SET is deliberately issued as typed RESP arguments. The password
	// is never interpolated into a command line or logged by the agent.
	for _, pair := range [][2]string{{"masteruser", req.GetSourceUser()}, {"masterauth", req.GetSourcePassword()}} {
		if _, err := target.Do(ctx, "CONFIG", "SET", pair[0], pair[1]); err != nil {
			return errf("INVALID", "configure replication %s: %v", pair[0], err)
		}
	}
	if _, err := target.Do(ctx, "REPLICAOF", host, strconv.Itoa(port)); err != nil {
		return errf("INTERNAL", "start native replication: %v", err)
	}
	// Persist the temporary relationship so an agent/process restart does not
	// silently turn an in-progress migration into a stale standalone target.
	if _, err := target.Do(ctx, "CONFIG", "REWRITE"); err != nil {
		return errf("INTERNAL", "persist native replication: %v", err)
	}
	if err := m.refreshManifest(st); err != nil {
		return errf("INTERNAL", "refresh manifest: %v", err)
	}
	return nil
}

func (m *Manager) clearNativeReplication(ctx context.Context, st *State, target *resp.Conn) {
	_, _ = target.Do(ctx, "REPLICAOF", "NO", "ONE")
	_, _ = target.Do(ctx, "CONFIG", "SET", "masterauth", "")
	_, _ = target.Do(ctx, "CONFIG", "SET", "masteruser", "")
	_, _ = target.Do(ctx, "CONFIG", "REWRITE")
	_ = m.refreshManifest(st)
}

func (m *Manager) waitNativeReplication(ctx context.Context, target, source *resp.Conn, requireCaughtUp bool) (replicationSnapshot, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastSource int64 = -1
	stable := 0
	for {
		targetInfo, targetErr := readReplicationSnapshot(ctx, target)
		if targetErr == nil && isReplicaRole(targetInfo.Role) && targetInfo.MasterLinkStatus == "up" && !targetInfo.SyncInProgress {
			if !requireCaughtUp {
				if sourceInfo, sourceErr := readReplicationSnapshot(ctx, source); sourceErr == nil {
					targetInfo.SourceOffset = sourceInfo.SourceOffset
				}
				return targetInfo, nil
			}
			sourceInfo, sourceErr := readReplicationSnapshot(ctx, source)
			if sourceErr == nil {
				if targetInfo.ReplicaOffset >= sourceInfo.SourceOffset && sourceInfo.SourceOffset == lastSource {
					stable++
				} else {
					stable = 0
				}
				lastSource = sourceInfo.SourceOffset
				targetInfo.SourceOffset = sourceInfo.SourceOffset
				if stable >= 2 {
					return targetInfo, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return replicationSnapshot{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func sourceMatches(info replicationSnapshot, address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.Trim(info.MasterHost, "[]"), strings.Trim(host, "[]")) && int64(info.MasterPort) == atoi(port)
}

func (m *Manager) promoteNativeReplication(ctx context.Context, st *State, target *resp.Conn) error {
	if _, err := target.Do(ctx, "REPLICAOF", "NO", "ONE"); err != nil {
		return errf("INTERNAL", "promote target: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		info, err := readReplicationSnapshot(ctx, target)
		if err == nil && info.Role == "master" {
			if _, err := target.Do(ctx, "CONFIG", "SET", "masterauth", ""); err != nil {
				return errf("INTERNAL", "clear temporary masterauth: %v", err)
			}
			if _, err := target.Do(ctx, "CONFIG", "SET", "masteruser", ""); err != nil {
				return errf("INTERNAL", "clear temporary masteruser: %v", err)
			}
			if _, err := target.Do(ctx, "CONFIG", "REWRITE"); err != nil {
				return errf("INTERNAL", "persist promoted target: %v", err)
			}
			if err := m.refreshManifest(st); err != nil {
				return errf("INTERNAL", "refresh promoted manifest: %v", err)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errf("TIMEOUT", "target did not become master")
}

func (m *Manager) migrateByReplication(ctx context.Context, st *State, req *agentv1.Migrate, target, source *resp.Conn) ([]byte, error) {
	started := time.Now()
	pattern := req.GetPattern()
	if pattern == "" {
		pattern = "*"
	}
	targetBefore, err := readReplicationSnapshot(ctx, target)
	if err != nil {
		return nil, errf("INTERNAL", "inspect target replication: %v", err)
	}
	if targetBefore.ClusterEnabled {
		return nil, errf("UNSUPPORTED", "native incremental migration requires a non-cluster target instance")
	}
	if targetBefore.Role != "master" {
		return nil, errf("CONFLICT", "native incremental migration requires a standalone target; current role is %s", targetBefore.Role)
	}
	sourceBefore, err := readReplicationSnapshot(ctx, source)
	if err != nil {
		return nil, errf("INTERNAL", "inspect source replication: %v", err)
	}
	if sourceBefore.ClusterEnabled {
		return nil, errf("UNSUPPORTED", "native incremental migration does not support a clustered source node")
	}
	host, rawPort, _ := net.SplitHostPort(req.GetSourceAddress())
	port, _ := strconv.Atoi(rawPort)
	if err := m.configureNativeReplication(ctx, st, req, target, host, port); err != nil {
		return nil, err
	}
	keepReplica := false
	defer func() {
		if !keepReplica {
			m.clearNativeReplication(context.Background(), st, target)
		}
	}()
	info, err := m.waitNativeReplication(ctx, target, source, req.GetSourceQuiesced())
	if err != nil {
		return nil, errf("TIMEOUT", "wait native replication: %v", err)
	}
	report := migrateReport{JobID: req.GetJobId(), Mode: req.GetMode().String(), Strategy: "replication", SourceAddress: req.GetSourceAddress(), Pattern: pattern, Passes: 1, SourceQuiesced: req.GetSourceQuiesced(), FullSyncDone: true, SourceOffset: info.SourceOffset, TargetOffset: info.ReplicaOffset, DurationSeconds: int64(time.Since(started) / time.Second)}
	if !req.GetSourceQuiesced() {
		keepReplica = true
		report.AwaitingCutover = true
		b, _ := json.Marshal(report)
		return b, nil
	}
	if err := m.promoteNativeReplication(ctx, st, target); err != nil {
		return nil, err
	}
	report.CutoverReady = true
	b, err := json.Marshal(report)
	return b, err
}

func (m *Manager) finalizeNativeMigration(ctx context.Context, st *State, req *agentv1.Migrate, target, source *resp.Conn) ([]byte, error) {
	started := time.Now()
	info, err := readReplicationSnapshot(ctx, target)
	if err != nil {
		return nil, errf("INTERNAL", "inspect target replication: %v", err)
	}
	if !isReplicaRole(info.Role) || !sourceMatches(info, req.GetSourceAddress()) {
		return nil, errf("CONFLICT", "target is not replicating from the requested source")
	}
	if !req.GetSourceQuiesced() {
		return nil, errf("INVALID", "source_quiesced must be true before cutover")
	}
	info, err = m.waitNativeReplication(ctx, target, source, true)
	if err != nil {
		return nil, errf("TIMEOUT", "wait final replication offset: %v", err)
	}
	if err := m.promoteNativeReplication(ctx, st, target); err != nil {
		return nil, err
	}
	report := migrateReport{JobID: req.GetJobId(), Mode: agentv1.Migrate_FULL_INCREMENTAL.String(), Strategy: "replication", SourceAddress: req.GetSourceAddress(), SourceQuiesced: true, FullSyncDone: true, CutoverReady: true, SourceOffset: info.SourceOffset, TargetOffset: info.ReplicaOffset, DurationSeconds: int64(time.Since(started) / time.Second)}
	b, err := json.Marshal(report)
	return b, err
}
