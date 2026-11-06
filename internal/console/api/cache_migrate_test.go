package api

import (
	"encoding/json"
	"testing"
)

func TestMergeMigrationMetaPreservesAgentCutoverReport(t *testing.T) {
	progress := mergeMigrationMeta(`{"strategy":"replication","awaiting_cutover":true}`, migrationMeta{
		InstanceID: 42, SourceUser: "migration", Pattern: "*", Mode: "full_incremental", Batch: 200, MaxPasses: 2,
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(progress), &got); err != nil {
		t.Fatal(err)
	}
	if got["strategy"] != "replication" || got["awaiting_cutover"] != true || got["instance_id"] != float64(42) || got["source_user"] != "migration" {
		t.Fatalf("metadata/report was not preserved: %v", got)
	}
}
