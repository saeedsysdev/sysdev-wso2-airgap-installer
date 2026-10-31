package wizard

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/app"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/auditlog"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/cluster"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/imagearchive"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

func TestUpstreamChangeInvalidatesInventory(t *testing.T) {
	s := State{RegistryHost: "registry.example", Project: "team", AuthMode: "basic", Username: "operator", Password: "secret", Revision: 8, Inventory: []registry.Image{{Reference: "registry.example/team/app:v1", Status: registry.Found}}, InventoryRevision: 8, InventoryStatus: "PASSED"}
	s.Set("project", "other")
	if s.Revision != 9 || s.InventoryStatus != "STALE" || len(s.Inventory) != 0 || s.InventoryRevision != 0 {
		t.Fatalf("inventory was not invalidated: %#v", s)
	}
	if s.Password != "secret" {
		t.Fatal("editing a project should preserve unrelated session input")
	}
}

func TestChangingAuthModeClearsPriorCredential(t *testing.T) {
	s := State{AuthMode: "basic", Username: "u", Password: "p", Token: "t"}
	s.Set("auth", "anonymous")
	if s.Username != "" || s.Password != "" || s.Token != "" {
		t.Fatal("credential material survived auth mode change")
	}
	if c := s.Credentials(); c.Username != "" || c.Password != "" || c.Token != "" {
		t.Fatal("anonymous mode returned credentials")
	}
}

func TestOutdatedCheckResultDoesNotReplaceNewState(t *testing.T) {
	s := State{RegistryHost: "registry.example", Revision: 4, InventoryStatus: "STALE"}
	if s.InventoryStatus == "RUNNING" {
		t.Fatal("test precondition invalid")
	}
	// The model applies results only while the same revision is still running.
	m := &Model{state: s}
	m.checkGeneration = 2
	_, _ = m.Update(checkedMsg{revision: 4, generation: 1, images: []registry.Image{{Status: registry.Found}}})
	if len(m.state.Inventory) != 0 || m.state.InventoryStatus != "STALE" {
		t.Fatalf("outdated result was accepted: %#v", m.state)
	}
}

func TestClusterAndWorkerEditsInvalidateOnlyDependentInventory(t *testing.T) {
	archive := &imagearchive.Result{BundleID: "verified"}
	s := State{Revision: 5, RegistryHost: "registry.example", Project: "team", Inventory: []registry.Image{{Status: registry.Found}}, InventoryRevision: 5, InventoryStatus: "PASSED", Cluster: &cluster.Inventory{Context: "old"}, Archive: archive}
	s.Set("context", "new-context")
	if s.Cluster != nil || len(s.Inventory) != 0 || s.InventoryStatus != "STALE" || s.Archive != archive {
		t.Fatalf("context edit invalidated unrelated archive or kept dependent results: %#v", s)
	}

	s = State{Revision: 9, RegistryHost: "registry.example", Project: "team", Inventory: []registry.Image{{Status: registry.Found}}, InventoryRevision: 9, InventoryStatus: "PASSED", Cluster: &cluster.Inventory{Context: "selected"}, Archive: archive}
	s.Set("workers", "worker-a,worker-b")
	if s.Cluster != nil || len(s.Inventory) != 0 || s.InventoryStatus != "STALE" || s.Archive != archive {
		t.Fatalf("worker edit did not invalidate platform/scheduling while retaining archive evidence: %#v", s)
	}
}

func TestBundleEditInvalidatesLocalAndReviewResultsButKeepsIndependentInputs(t *testing.T) {
	images := []registry.Image{{Component: "registry-result", Status: registry.Found}}
	clusterInfo := &cluster.Inventory{Context: "context-a"}
	archive := &imagearchive.Result{BundleID: "old"}
	s := State{Revision: 3, RegistryHost: "registry.example", Project: "team", BundlePath: "old", Inventory: images, InventoryRevision: 3, InventoryStatus: "PASSED", Cluster: clusterInfo, Archive: archive}
	s.Set("bundle", "new")
	if s.InventoryStatus != "STALE" || s.InventoryRevision != 0 || s.Archive != nil || s.Cluster != clusterInfo || len(s.Inventory) != 1 || s.Inventory[0].Status != registry.Found {
		t.Fatalf("bundle edit invalidated unrelated results or kept stale local review: %#v", s)
	}
}

func TestCanceledCheckAndNewRevisionCannotApplyOldResult(t *testing.T) {
	log, err := auditlog.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	m := &Model{service: app.New(registry.NewClient(), log), state: State{RegistryHost: "registry.example", Revision: 10, InventoryStatus: "RUNNING"}, checking: true, checkGeneration: 4}
	m.state.Set("bundle", "new-bundle")
	_, _ = m.Update(checkedMsg{revision: 10, generation: 4, images: []registry.Image{{Status: registry.Found}}})
	if m.state.InventoryStatus != "STALE" || len(m.state.Inventory) != 0 || m.state.Revision != 11 {
		t.Fatalf("obsolete asynchronous result replaced newer configuration state: %#v", m.state)
	}
}

func TestEscCancelsActivePreflightAndRejectsLateResult(t *testing.T) {
	log, err := auditlog.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	canceled := false
	m := &Model{service: app.New(registry.NewClient(), log), state: State{RegistryHost: "registry.example", Revision: 10, InventoryStatus: "RUNNING"}, checking: true, checkGeneration: 4, checkCancel: func() { canceled = true }}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '\x1b'}))
	if !canceled || m.checking || m.state.InventoryStatus != "STALE" || m.checkGeneration != 5 {
		t.Fatalf("active check was not canceled and marked stale: canceled=%t model=%#v", canceled, m)
	}
	_, _ = m.Update(checkedMsg{revision: 10, generation: 4, images: []registry.Image{{Status: registry.Found}}})
	if len(m.state.Inventory) != 0 || m.state.InventoryStatus != "STALE" {
		t.Fatal("late canceled check replaced the current inventory")
	}
}

func TestBackAndEditRestoreLocationAndInvalidateResults(t *testing.T) {
	log, err := auditlog.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	m := &Model{service: app.New(registry.NewClient(), log), step: 11, state: State{RegistryHost: "registry.example", Project: "old-project", AuthMode: "anonymous", Revision: 2, InventoryStatus: "PASSED", InventoryRevision: 2, Inventory: []registry.Image{{Status: registry.Found}}}}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '\x1b'}))
	if m.step != 10 {
		t.Fatalf("Back from inventory landed on step %d, want bundle step 10", m.step)
	}
	for m.step > 5 {
		_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '\x1b'}))
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '\x1b'}))
	if m.step != 2 {
		t.Fatalf("Back from anonymous inventory landed on step %d, want auth step 2", m.step)
	}
	m.step = -1
	m.returnStep = 12
	m.editing = true
	m.editMode = true
	m.input = "new-project"
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'}))
	if m.step != 1 {
		t.Fatalf("selected edit step %d, want project step 1", m.step)
	}
	for range len(m.input) {
		_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	}
	for _, r := range "new-project" {
		_, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: string(r), Code: r}))
	}
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.step != 11 || !m.checking || cmd == nil || m.state.Project != "new-project" {
		t.Fatalf("stale edit did not return to inventory and start a fresh check: step=%d checking=%t state=%#v", m.step, m.checking, m.state)
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '\x1b'}))
	if m.state.InventoryStatus != "STALE" || len(m.state.Inventory) != 0 {
		t.Fatalf("edit did not invalidate inventory: %#v", m.state)
	}
}

func TestPasswordIsMaskedInView(t *testing.T) {
	m := &Model{step: 4, input: "canary-visible-password", noColor: true}
	out := m.View().Content
	if strings.Contains(out, m.input) {
		t.Fatal("password appeared in rendered view")
	}
	if !strings.Contains(out, "Password: "+strings.Repeat("*", len(m.input))) {
		t.Fatal("password mask was not rendered")
	}
}

func TestReviewWrapsForStandardTerminalWidth(t *testing.T) {
	m := NewDemo(nil, true, "C:/Users/operator/AppData/Local/SaeedSysDev/Sysdev Wso2 Airgap Installer/logs/run-20261031-123456.log")
	m.step = 12
	m.width = 80
	m.height = 100
	whole := m.View().Content
	if !strings.Contains(whole, "DEMO ONLY") || !strings.Contains(whole, "NON-MUTATING REVIEW PLAN") {
		t.Fatal("demo fixture/review labels are missing")
	}
	if !strings.Contains(whole, "WSO2 API Manager 4.7.0") || !strings.Contains(whole, "30043 -> 9443") || !strings.Contains(whole, "docker.io/wso2/wso2am-acp:4.7.0") {
		t.Fatal("review does not carry the versioned profile, port, and source-image details")
	}
	m.height = 18
	for _, line := range strings.Split(m.View().Content, "\n") {
		if len([]rune(line)) > 80 {
			t.Fatalf("review line exceeds 80 columns (%d): %q", len([]rune(line)), line)
		}
	}
	if !strings.Contains(m.View().Content, "Developed and Supported by SaeedSysDev") {
		t.Fatal("footer attribution is not retained while scrolling")
	}
}

func TestInventoryUsesResponsiveImageStatusTableAndKeepsLogVisible(t *testing.T) {
	m := NewDemo(nil, true, "C:/Users/operator/AppData/Local/SaeedSysDev/logs/run.log")
	m.width = 80
	m.height = 120
	view := m.View().Content
	for _, label := range []string{"COMPONENT", "REMOTE", "LOCAL", "INTEGRITY", "PLATFORM", "Log: C:/Users/operator/AppData/Local/SaeedSysDev/logs/run.log"} {
		if !strings.Contains(view, label) {
			t.Fatalf("inventory table/log footer omitted %q", label)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > m.width {
			t.Fatalf("80-column inventory line overflows: %d: %q", len([]rune(line)), line)
		}
	}
	m.width = 52
	view = m.View().Content
	if !strings.Contains(view, "IMAGE STATUS (compact layout)") || !strings.Contains(view, "DEMO ONLY") {
		t.Fatal("narrow terminal did not switch to responsive compact image status")
	}
	if !strings.Contains(view, "Log:") || !strings.Contains(strings.ReplaceAll(view, "\n", ""), "C:/Users/operator/AppData/Local/SaeedSysDev/logs/run.log") {
		t.Fatalf("log path is not persistent in the narrow inventory view: %s", view)
	}
}
