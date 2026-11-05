package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestTaskAssetSharingLiveToolPolicy(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("tool sharing", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	assets := d.Assets()
	company, _, err := d.Companies().UpsertCompany(fmt.Sprintf("sharing-tool-%d", task.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Companies().DeleteCompany(company)
	foreign, err := assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: fmt.Sprintf("sharing-tool-%d.invalid", task.ID)})
	if err != nil {
		t.Fatal(err)
	}
	defer assets.DeleteByIDs([]int64{foreign})
	if _, err = d.Exec(`UPDATE assets SET company_id=$2 WHERE id=$1`, foreign, company); err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	tools.SetTaskID(task.ID)
	tools.SetAssetStore(assets, d.Companies())
	idJSON, _ := json.Marshal(foreign)
	intent := intentItem{Summary: "reference foreign target", AssetIDs: []json.RawMessage{idJSON, idJSON}}
	if _, err := tools.addOneIntent(intent); err == nil {
		t.Fatal("foreign intent should fail while sharing off")
	}
	query := fmt.Sprintf(`{"id":%d}`, foreign)
	if got := callReadJSON(t, tools.listAssets(), query); strings.Contains(fmt.Sprint(got), fmt.Sprintf("sharing-tool-%d.invalid", task.ID)) {
		t.Fatalf("foreign asset leaked: %v", got)
	}
	for _, enabled := range []bool{true, false} {
		if _, err := d.UpdateTask(task.ID, db.TaskPatch{ShareGlobalAssets: &enabled}); err != nil {
			t.Fatal(err)
		}
		got := callReadJSON(t, tools.listAssets(), query)
		visible := strings.Contains(fmt.Sprint(got), fmt.Sprintf("sharing-tool-%d.invalid", task.ID))
		if visible != enabled {
			t.Fatalf("live list_assets enabled=%v result=%v", enabled, got)
		}
		companies, err := d.Companies().ListCompaniesForTask(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range companies {
			if c.ID == company {
				found = true
			}
		}
		if found != enabled {
			t.Fatalf("live company policy enabled=%v found=%v", enabled, found)
		}
	}
	enabled := true
	if _, err := d.UpdateTask(task.ID, db.TaskPatch{ShareGlobalAssets: &enabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.addOneIntent(intent); err != nil {
		t.Fatalf("explicit opt-in should allow target and duplicate IDs: %v", err)
	}
}
