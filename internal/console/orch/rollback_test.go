package orch

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
)

// seedParamRevision 直接写入一个参数版本（测试夹具，不经过变更流程）。
func seedParamRevision(t *testing.T, st *store.Store, id string, unitID uint, parent string, params map[string]string) {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	rev := &store.ParamRevision{ID: id, UnitID: unitID, ParentID: parent, Params: string(b), Note: "seed", CreatedBy: "admin", CreatedAt: time.Now()}
	if err := st.DB.Create(rev).Error; err != nil {
		t.Fatal(err)
	}
}

func countParamRevisions(t *testing.T, st *store.Store, unitID uint) int64 {
	t.Helper()
	var n int64
	if err := st.DB.Model(&store.ParamRevision{}).Where("unit_id = ?", unitID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// rollbackFixture: 部署单元 A 的版本链 p1 -> p2（当前）；单元 B 有自己的版本 p10。
// RequireApproval 打开，变更只写入版本与操作记录，不下发到实例。
func rollbackFixture(t *testing.T) (*Service, *store.Store, uint) {
	t.Helper()
	st := storetest.Open(t)
	s := &Service{Store: st, RequireApproval: true}
	a := &store.Unit{Name: "cache-a", Mode: "standalone", MemoryMB: 100, Shards: 1, RevisionID: "p00000002"}
	if err := st.DB.Create(a).Error; err != nil {
		t.Fatal(err)
	}
	b := &store.Unit{Name: "cache-b", Mode: "standalone", MemoryMB: 100, Shards: 1, RevisionID: "p00000010"}
	if err := st.DB.Create(b).Error; err != nil {
		t.Fatal(err)
	}
	seedParamRevision(t, st, "p00000001", a.ID, "", map[string]string{"maxclients": "100", "hz": "10"})
	seedParamRevision(t, st, "p00000002", a.ID, "p00000001", map[string]string{"maxclients": "2000", "timeout": "30"})
	seedParamRevision(t, st, "p00000010", b.ID, "", map[string]string{"maxclients": "100"})
	return s, st, a.ID
}

func TestRollbackParamsAppendsNewRevisionWithTargetContent(t *testing.T) {
	s, st, unitID := rollbackFixture(t)
	op, err := s.RollbackParams(unitID, "p00000001", ParamChange{ParentRevisionID: "p00000002", Note: "回滚到 p00000001"}, "admin")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if op == nil || op.ID == 0 {
		t.Fatal("rollback did not record an operation")
	}
	var revs []store.ParamRevision
	if err := st.DB.Where("unit_id = ?", unitID).Order("seq").Find(&revs).Error; err != nil {
		t.Fatal(err)
	}
	if len(revs) != 3 {
		t.Fatalf("want a new revision appended (3 total), got %d", len(revs))
	}
	if revs[0].ID != "p00000001" || revs[1].ID != "p00000002" {
		t.Fatal("history was rewritten")
	}
	got := revs[2]
	if got.ParentID != "p00000002" || got.CreatedBy != "admin" {
		t.Fatalf("new revision parent/author = %q/%q", got.ParentID, got.CreatedBy)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(got.Params), &params); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(params, map[string]string{"maxclients": "100", "hz": "10"}) {
		t.Fatalf("new revision content = %v, want the target revision's full set", params)
	}
}

func TestRollbackParamsRejectsRevisionOfAnotherUnit(t *testing.T) {
	s, st, unitID := rollbackFixture(t)
	before := countParamRevisions(t, st, unitID)
	_, err := s.RollbackParams(unitID, "p00000010", ParamChange{ParentRevisionID: "p00000002", Note: "x"}, "admin")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revision of another unit: err = %v, want store.ErrNotFound", err)
	}
	if countParamRevisions(t, st, unitID) != before {
		t.Fatal("a revision was created for a foreign revision")
	}
}

func TestRollbackParamsRejectsStaleParentRevision(t *testing.T) {
	s, st, unitID := rollbackFixture(t)
	before := countParamRevisions(t, st, unitID)
	_, err := s.RollbackParams(unitID, "p00000001", ParamChange{ParentRevisionID: "p00000001", Note: "x"}, "admin")
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale parent: err = %v, want ErrRevisionConflict", err)
	}
	if countParamRevisions(t, st, unitID) != before {
		t.Fatal("a revision was created despite the parent conflict")
	}
}

func TestRollbackParamsRejectsSameRevision(t *testing.T) {
	s, _, unitID := rollbackFixture(t)
	_, err := s.RollbackParams(unitID, "p00000002", ParamChange{ParentRevisionID: "p00000002", Note: "x"}, "admin")
	if !errors.Is(err, ErrSameRevision) {
		t.Fatalf("target == current: err = %v, want ErrSameRevision", err)
	}
}

func TestRollbackParamsRequiresParentRevisionAndNote(t *testing.T) {
	s, _, unitID := rollbackFixture(t)
	if _, err := s.RollbackParams(unitID, "p00000001", ParamChange{Note: "x"}, "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing parent_revision_id: err = %v, want ErrInvalid", err)
	}
	if _, err := s.RollbackParams(unitID, "p00000001", ParamChange{ParentRevisionID: "p00000002"}, "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing note: err = %v, want ErrInvalid", err)
	}
}

func TestRollbackParamsRejectsParamOutsideCatalog(t *testing.T) {
	s, st, unitID := rollbackFixture(t)
	seedParamRevision(t, st, "p00000004", unitID, "", map[string]string{"no-such-param": "1"})
	before := countParamRevisions(t, st, unitID)
	_, err := s.RollbackParams(unitID, "p00000004", ParamChange{ParentRevisionID: "p00000002", Note: "x"}, "admin")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "no-such-param") {
		t.Fatalf("catalog-unknown param in history: err = %v, want an explicit error naming the key", err)
	}
	if countParamRevisions(t, st, unitID) != before {
		t.Fatal("a revision was created with an unmanaged parameter")
	}
}
