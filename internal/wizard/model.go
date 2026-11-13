package wizard

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/app"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/cluster"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/domain"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/imagearchive"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/oracleprep"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/plan"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/preparation"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

type checkedMsg struct {
	revision     uint64
	generation   uint64
	images       []registry.Image
	cluster      *cluster.Inventory
	clusterError string
	archive      *imagearchive.Result
	archiveError string
	err          error
}

type preparationFinishedMsg struct {
	generation uint64
	revision   uint64
	report     preparation.Report
	err        error
}

type Model struct {
	service                                          *app.Service
	state                                            State
	step, editTarget                                 int
	editing, editMode, checking, done, noColor, demo bool
	preparing                                        bool
	input, errText, logPath                          string
	width, height                                    int
	ctx                                              context.Context
	checkCancel                                      context.CancelFunc
	preparationCancel                                context.CancelFunc
	preparationGeneration                            uint64
	oracleValidator                                  oracleprep.Validator
	editTargetPosition                               int
	returnStep                                       int
	checkGeneration                                  uint64
	scroll                                           int
}

var labels = []string{"Registry host", "Project prefix", "Authentication", "Username or token", "Password", "Kubeconfig path (optional)", "Kubernetes context (optional)", "Target namespace (optional)", "Selected workers (comma-separated, optional)", "StorageClass (optional)", "Offline bundle directory (optional)", "Preflight inventory", "Review plan"}

func New(service *app.Service, noColor bool, logPath string) *Model {
	return &Model{service: service, noColor: noColor, logPath: logPath, ctx: context.Background(), state: State{AuthMode: "anonymous", StateDirectory: defaultStateDirectory()}}
}

func NewDemo(service *app.Service, noColor bool, logPath string) *Model {
	m := New(service, noColor, logPath)
	m.demo = true
	m.step = 11
	m.state.RegistryHost = "registry.demo.invalid"
	m.state.Project = "demo-fixtures"
	m.state.Revision = 1
	m.state.InventoryRevision = 1
	m.state.InventoryStatus = "DEMO ONLY"
	for _, required := range registry.Profile.Images {
		m.state.Inventory = append(m.state.Inventory, registry.Image{Component: required.Component, SourceReference: required.SourceReference, Reference: m.state.RegistryHost + "/" + m.state.Project + "/" + required.Repository + ":" + required.Tag, Status: "DEMO ONLY", Detail: "Deterministic local fixture; no registry request was made."})
	}
	return m
}
func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
	case checkedMsg:
		if v.generation != m.checkGeneration {
			return m, nil
		}
		m.checking = false
		m.checkCancel = nil
		if v.revision != m.state.Revision || m.state.InventoryStatus != "RUNNING" {
			return m, nil
		}
		m.state.Cluster = v.cluster
		m.state.ClusterError = v.clusterError
		m.state.Archive = v.archive
		m.state.ArchiveError = v.archiveError
		if v.err != nil {
			m.errText = v.err.Error()
			m.state.InventoryStatus = "FAILED"
			m.service.Log.Event("ERROR", "wizard", "Registry validation failed", map[string]string{"revision": fmt.Sprint(v.revision), "error": v.err.Error()})
		} else {
			m.state.Inventory = v.images
			m.state.InventoryRevision = v.revision
			m.state.InventoryStatus = "PASSED"
			m.errText = ""
		}
	case preparationFinishedMsg:
		if v.generation != m.preparationGeneration || v.revision != m.state.PreparationRevision || m.state.PreparationStatus != "RUNNING" {
			return m, nil
		}
		m.preparing = false
		m.preparationCancel = nil
		if v.err != nil {
			if errors.Is(v.err, context.Canceled) {
				m.state.PreparationStatus = "CANCELED"
				m.errText = "Local preparation canceled; no successful export was recorded."
			} else {
				m.state.PreparationStatus = "FAILED"
				m.errText = "Local preparation failed: " + v.err.Error()
			}
			m.service.Log.Event("ERROR", "stage3-prepare", "Local preparation failed or was canceled", map[string]string{"revision": fmt.Sprint(v.revision), "canceled": fmt.Sprint(errors.Is(v.err, context.Canceled))})
		} else {
			m.state.PreparationStatus = v.report.FileGenerationStatus
			m.state.PreparationReport = &v.report
			m.errText = ""
			m.service.Log.Event("INFO", "stage3-prepare", "Local preparation finished", map[string]string{"revision": fmt.Sprint(v.revision), "file_generation": v.report.FileGenerationStatus, "deployment_readiness": v.report.DeploymentReadiness, "blockers": fmt.Sprint(len(v.report.Blockers))})
		}
	case tea.KeyPressMsg:
		key := v.String()
		if key == "ctrl+c" {
			if m.checkCancel != nil {
				m.checkCancel()
			}
			if m.preparationCancel != nil {
				m.preparationCancel()
			}
			m.done = true
			return m, tea.Quit
		}
		if key == "f1" {
			m.errText = "Help: Enter submits or continues, Backspace edits text, Esc goes back, Ctrl-E chooses a step, r retries validation, PageUp/PageDown scroll, Ctrl-C quits."
			return m, nil
		}
		if key == "pgup" || key == "pageup" {
			m.scroll = max(0, m.scroll-max(1, m.height/2))
			return m, nil
		}
		if key == "pgdown" || key == "pagedown" {
			m.scroll += max(1, m.height/2)
			return m, nil
		}
		if m.editing {
			return m, m.handleEditKey(v)
		}
		if m.checking {
			if key == "esc" || key == "escape" {
				if m.checkCancel != nil {
					m.checkCancel()
				}
				m.checkCancel = nil
				m.checkGeneration++
				m.checking = false
				m.state.InventoryStatus = "STALE"
				m.errText = "Registry validation canceled. Press r to retry or Esc to go back."
				return m, nil
			}
			return m, nil
		}
		if m.preparing {
			if key == "esc" || key == "escape" {
				if m.preparationCancel != nil {
					m.preparationCancel()
				}
				m.preparationCancel = nil
				m.preparationGeneration++
				m.preparing = false
				m.state.PreparationStatus = "CANCELED"
				m.errText = "Local preparation canceled; no successful export was recorded."
			}
			return m, nil
		}
		if key == "esc" || key == "escape" {
			m.back()
			return m, nil
		}
		if key == "ctrl+e" {
			m.editing = true
			m.editMode = true
			m.returnStep = m.step
			m.editTargetPosition = 0
			m.step = -1
			m.errText = "Edit: use Up/Down and Enter to choose a field; shortcuts 1-9, 0, B select the first 11 configuration fields. Escape returns."
			return m, nil
		}
		if key == "r" && m.step == 11 {
			return m, m.checkPlan()
		}
		if m.step == preparationReviewStep && key == "e" {
			if m.state.PreparationReport != nil {
				m.errText = "This revision already has a generated export. Edit the public output directory to create a separate export."
				return m, nil
			}
			return m, m.runPreparation()
		}
		if key == "enter" || key == "return" {
			return m, m.submit()
		}
		if m.step == 12 && key == "a" {
			m.errText = "Apply is unavailable in this milestone. No changes were made."
			return m, nil
		}
		if m.step >= 0 && (m.step < 11 || (m.step >= preparationFirstStep && m.step <= preparationLastField)) {
			if key == "backspace" {
				if len(m.input) > 0 {
					m.input = m.input[:len(m.input)-1]
				}
				return m, nil
			}
			if v.Key().Text != "" {
				m.input += v.Key().Text
			}
		}
	}
	return m, nil
}

func (m *Model) handleEditKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.step == -1 {
		if key == "esc" || key == "escape" {
			m.editing = false
			m.editMode = false
			m.step = m.returnStep
			m.errText = ""
			return nil
		}
		steps := m.editableSteps()
		if len(steps) == 0 {
			return nil
		}
		if key == "down" || key == "j" {
			m.editTargetPosition = min(m.editTargetPosition+1, len(steps)-1)
			return nil
		}
		if key == "up" || key == "k" {
			m.editTargetPosition = max(m.editTargetPosition-1, 0)
			return nil
		}
		if key == "enter" || key == "return" {
			m.step = steps[m.editTargetPosition]
			m.input = m.valueFor(m.step)
			if m.step >= preparationFirstStep {
				m.input = m.preparationValue(m.step)
			}
			m.errText = ""
			return nil
		}
		if (key >= "1" && key <= "9") || key == "0" || key == "b" || key == "B" {
			choice := editNumber(key) - 1
			if choice >= 0 && choice < 11 {
				m.step = choice
				m.input = m.valueFor(m.step)
				m.errText = ""
			}
		}
		return nil
	}
	if key == "esc" || key == "escape" {
		m.editing = false
		m.editMode = false
		m.step = m.returnStep
		m.errText = ""
		return nil
	}
	if key == "enter" || key == "return" {
		return m.submit()
	}
	if key == "backspace" || key == "delete" {
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
		return nil
	}
	if msg.Key().Text != "" {
		m.input += msg.Key().Text
	}
	return nil
}

func (m *Model) valueFor(step int) string {
	switch step {
	case 0:
		return m.state.RegistryHost
	case 1:
		return m.state.Project
	case 2:
		return m.state.AuthMode
	case 3:
		if m.state.AuthMode == "token" {
			return m.state.Token
		}
		return m.state.Username
	case 4:
		return m.state.Password
	case 5:
		return m.state.KubeconfigPath
	case 6:
		return m.state.ContextName
	case 7:
		return m.state.Namespace
	case 8:
		return m.state.WorkerNames
	case 9:
		return m.state.StorageClass
	case 10:
		return m.state.BundlePath
	}
	return ""
}

func (m *Model) submit() tea.Cmd {
	if m.step < 5 {
		field := []string{"registry", "project", "auth", "username", "password"}[m.step]
		value := strings.TrimSpace(m.input)
		if m.step == 0 || m.step == 1 {
			if err := registry.ValidateEndpoint(func() string {
				if m.step == 0 {
					return value
				}
				return m.state.RegistryHost
			}(), func() string {
				if m.step == 1 {
					return value
				}
				return m.state.Project
			}()); err != nil {
				m.errText = err.Error()
				return nil
			}
		}
		if m.step == 2 {
			value = strings.ToLower(value)
			if value != "" && value != "anonymous" && value != "basic" && value != "token" {
				m.errText = "Choose anonymous, basic, or token."
				return nil
			}
			if value == "" {
				value = "anonymous"
			}
		}
		if m.step == 3 && m.state.AuthMode == "basic" && value == "" {
			m.errText = "A username is required for basic authentication."
			return nil
		}
		if m.step == 4 && m.state.AuthMode == "basic" && value == "" {
			m.errText = "A password is required for basic authentication."
			return nil
		}
		if m.step == 3 && m.state.AuthMode == "token" {
			field = "token"
		}
		m.state.Set(field, value)
		m.service.Log.Event("INFO", "wizard-step", "Configuration value updated", map[string]string{"step": labels[m.step], "revision": fmt.Sprint(m.state.Revision)})
		m.errText = ""
		if m.editMode {
			m.editing = false
			m.editMode = false
			m.step = m.returnStep
			m.input = ""
			if m.step == 12 && !m.demo && (m.state.InventoryRevision != m.state.Revision || m.state.InventoryStatus != "PASSED") {
				m.step = 11
				return m.checkPlan()
			}
			return nil
		}
		if m.step == 2 && m.state.AuthMode == "anonymous" {
			m.state.Set("username", "")
			m.state.Set("password", "")
			m.step = 5
			m.input = ""
			m.input = m.valueFor(m.step)
			return nil
		}
		if m.step == 3 && m.state.AuthMode == "token" {
			m.step = 5
			m.input = m.valueFor(m.step)
			return nil
		}
		if m.step == 4 {
			m.step = 5
			m.input = m.valueFor(m.step)
			return nil
		}
		m.step++
		m.input = m.valueFor(m.step)
		return nil
	}
	if m.step >= 5 && m.step < 11 {
		fields := []string{"kubeconfig", "context", "namespace", "workers", "storage", "bundle"}
		field := fields[m.step-5]
		value := strings.TrimSpace(m.input)
		m.state.Set(field, value)
		m.service.Log.Event("INFO", "wizard-step", "Preflight input updated", map[string]string{"step": labels[m.step], "revision": fmt.Sprint(m.state.Revision)})
		m.errText = ""
		if m.editMode {
			m.editing = false
			m.editMode = false
			m.step = m.returnStep
			m.input = ""
			return nil
		}
		if m.step == 5 && value == "" {
			m.step = 10
		} else {
			m.step++
		}
		m.input = m.valueFor(m.step)
		if m.step == 11 {
			return m.checkPlan()
		}
		return nil
	}
	if m.step >= preparationFirstStep && m.step <= preparationLastField {
		if err := m.setPreparationValue(m.step, m.input); err != nil {
			m.errText = err.Error()
			return nil
		}
		m.errText = ""
		m.service.Log.Event("INFO", "stage3-input", "Preparation input updated", map[string]string{"field": labels[m.step], "revision": fmt.Sprint(m.state.PreparationRevision)})
		if m.editMode {
			m.editing = false
			m.editMode = false
			m.step = m.returnStep
			m.input = ""
			return nil
		}
		m.step = m.nextPreparationStep(m.step)
		if m.step <= preparationLastField {
			m.input = m.preparationValue(m.step)
		} else {
			m.input = ""
		}
		return nil
	}
	if m.step == 11 {
		if m.demo {
			m.step = 12
			m.input = ""
			return nil
		}
		if m.state.InventoryRevision != m.state.Revision || m.state.InventoryStatus != "PASSED" {
			m.errText = "Run the read-only registry, archive, and Kubernetes preflight before reviewing the plan."
			return m.checkPlan()
		}
		m.step = 12
		m.input = ""
		return nil
	}
	if m.step == 12 {
		if m.demo {
			m.done = true
			return tea.Quit
		}
		m.step = preparationFirstStep
		m.input = m.preparationValue(m.step)
		return nil
	}
	if m.step == preparationReviewStep {
		m.done = true
		return tea.Quit
	}
	return nil
}

func (m *Model) checkPlan() tea.Cmd {
	if m.state.RegistryHost == "" {
		m.errText = "Enter a registry host before checking images."
		m.step = 0
		m.input = ""
		return nil
	}
	m.checking = true
	m.state.InventoryStatus = "RUNNING"
	m.checkGeneration++
	rev := m.state.Revision
	generation := m.checkGeneration
	host, project, cred := m.state.RegistryHost, m.state.Project, m.state.Credentials()
	kubeconfig, contextName, namespace, storageClass, bundlePath := m.state.KubeconfigPath, m.state.ContextName, m.state.Namespace, m.state.StorageClass, m.state.BundlePath
	workerNames := splitList(m.state.WorkerNames)
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.checkCancel = cancel
	return func() tea.Msg {
		c, timeoutCancel := context.WithTimeout(ctx, 10*time.Minute)
		defer timeoutCancel()
		msg := checkedMsg{revision: rev, generation: generation}
		var clusterInfo *cluster.Inventory
		if kubeconfig != "" {
			ports := make([]int, 0, len(registry.Profile.NodePorts))
			for _, port := range registry.Profile.NodePorts {
				ports = append(ports, port.Port)
			}
			clusterCtx, clusterCancel := context.WithTimeout(c, 3*time.Minute)
			inv, e := cluster.Discover(clusterCtx, kubeconfig, contextName, namespace, cluster.PreflightOptions{Namespace: namespace, SelectedWorkers: workerNames, StorageClass: storageClass, NodePorts: ports})
			clusterCancel()
			if e != nil {
				msg.clusterError = e.Error()
			} else {
				clusterInfo = &inv
				msg.cluster = clusterInfo
			}
		} else if contextName != "" || namespace != "" || len(workerNames) > 0 || storageClass != "" {
			msg.clusterError = "Kubernetes context, namespace, worker, or StorageClass input requires a kubeconfig path."
		}
		platforms := []registry.Platform{}
		if clusterInfo != nil && clusterInfo.Preflight.SelectedWorkerStatus.Status == "READY" {
			platforms = cluster.PlatformsForWorkers(*clusterInfo, clusterInfo.Preflight.SelectedWorkers)
		}
		images, e := m.service.CheckImages(c, host, project, cred, rev, platforms)
		msg.images = images
		msg.err = e
		if bundlePath != "" {
			archive, archiveErr := imagearchive.Verify(c, bundlePath, registry.Profile.Images)
			if archiveErr != nil {
				msg.archiveError = archiveErr.Error()
			} else {
				for i := range archive.Images {
					archive.Images[i].CheckPlatforms(platforms)
				}
				msg.archive = &archive
			}
		}
		return msg
	}
}

func splitList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}
func editNumber(key string) int {
	if key == "0" {
		return 10
	}
	if key == "b" || key == "B" {
		return 11
	}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return int(key[0] - '0')
	}
	return 0
}

func (m *Model) back() {
	if m.step == preparationReviewStep {
		m.step = m.previousPreparationStep(m.step)
		m.input = m.preparationValue(m.step)
		m.errText = ""
		return
	}
	if m.step >= preparationFirstStep {
		m.step = m.previousPreparationStep(m.step)
		if m.step >= preparationFirstStep {
			m.input = m.preparationValue(m.step)
		} else {
			m.input = ""
		}
		m.errText = ""
		return
	}
	if m.step == 11 {
		m.step = 10
		m.input = m.valueFor(m.step)
		m.errText = ""
		return
	}
	if m.step == 12 {
		m.step = 11
		m.errText = ""
		return
	}
	if m.step == 5 {
		switch m.state.AuthMode {
		case "basic":
			m.step = 4
		case "token":
			m.step = 3
		default:
			m.step = 2
		}
		m.input = m.valueFor(m.step)
		m.errText = ""
		return
	}
	if m.step > 0 && m.step <= 10 {
		m.step--
		m.input = m.valueFor(m.step)
		m.errText = ""
		return
	}
}

func (m *Model) View() tea.View {
	var b strings.Builder
	if m.demo {
		writeWrapped(&b, "DEMO MODE - FIXTURE DATA ONLY; no network checks or installation operations are performed.", m.width, "")
	}
	header := domain.ApplicationName
	if !m.noColor {
		header = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Render(header)
	}
	fmt.Fprintf(&b, "%s  0.1.0-dev\n", header)
	if m.step < 0 {
		b.WriteString("Edit configuration step\n")
	} else {
		fmt.Fprintf(&b, "Step %d of %d  |  %s\n", m.step+1, len(labels), labels[m.step])
	}
	b.WriteString(strings.Repeat("-", min(max(m.width, 24), 72)) + "\n\n")
	switch m.step {
	case -1:
		b.WriteString("Choose a field to edit:\n")
		steps := m.editableSteps()
		start := max(0, min(m.editTargetPosition-5, len(steps)-14))
		end := min(len(steps), start+14)
		for pos := start; pos < end; pos++ {
			prefix := "  "
			if pos == m.editTargetPosition {
				prefix = "> "
			}
			fmt.Fprintf(&b, "%s%s\n", prefix, labels[steps[pos]])
		}
	case 0:
		writeWrapped(&b, "Registry host and optional port (for example: registry.internal:5000). Do not enter a URL scheme.", m.width, "")
		b.WriteByte('\n')
		writeWrapped(&b, "Registry: "+m.input, m.width, "")
	case 1:
		writeWrapped(&b, "Optional project/repository prefix. Leave blank to query profile repositories at the registry root.", m.width, "")
		b.WriteByte('\n')
		writeWrapped(&b, "Project: "+m.input, m.width, "")
	case 2:
		b.WriteString("Select registry authentication: anonymous, basic, or token.\n\n")
		fmt.Fprintf(&b, "Authentication: %s\n", m.input)
	case 3:
		if m.state.AuthMode == "token" {
			b.WriteString("Enter a short-lived read-only bearer token. It is held in memory for this run.\n\n")
			writeWrapped(&b, "Token: "+strings.Repeat("*", len([]rune(m.input))), m.width, "")
		} else {
			b.WriteString("Enter a read-only registry username.\n\n")
			writeWrapped(&b, "Username: "+m.input, m.width, "")
		}
	case 4:
		writeWrapped(&b, "Enter the registry password. It is held in memory for this run and never shown.", m.width, "")
		b.WriteByte('\n')
		writeWrapped(&b, "Password: "+strings.Repeat("*", len([]rune(m.input))), m.width, "")
	case 5:
		writeWrapped(&b, "Path to the user-selected kubeconfig. Press Enter blank to skip Kubernetes preflight.", m.width, "")
		b.WriteByte('\n')
		writeWrapped(&b, "Kubeconfig: "+m.input, m.width, "")
	case 6:
		writeWrapped(&b, "Optional context name. Leave blank to use the kubeconfig current-context.", m.width, "")
		b.WriteByte('\n')
		writeWrapped(&b, "Context: "+m.input, m.width, "")
	case 7:
		writeWrapped(&b, "Target namespace for authorization and resource preflight. Leave blank for context/default namespace.", m.width, "")
		b.WriteByte('\n')
		writeWrapped(&b, "Namespace: "+m.input, m.width, "")
	case 8:
		writeWrapped(&b, "Optional comma-separated worker node names. Leave blank to assess all eligible workers.", m.width, "")
		b.WriteByte('\n')
		writeWrapped(&b, "Workers: "+m.input, m.width, "")
	case 9:
		writeWrapped(&b, "Selected StorageClass name. Metadata is read only; provisioning and access modes remain unverified.", m.width, "")
		b.WriteByte('\n')
		writeWrapped(&b, "StorageClass: "+m.input, m.width, "")
	case 10:
		writeWrapped(&b, "Optional directory containing bundle.json and listed image archive files.", m.width, "")
		b.WriteByte('\n')
		writeWrapped(&b, "Offline bundle: "+m.input, m.width, "")
	case 11:
		b.WriteString("INTEGRATED PREFLIGHT INVENTORY\n\n")
		if m.checking {
			b.WriteString("Checking registry, optional bundle, and Kubernetes preflight. Press Esc to cancel.\n")
		} else if m.state.InventoryStatus != "PASSED" && !m.demo {
			b.WriteString("Inventory has not passed for this configuration. Press Enter or r to retry.\n")
		} else {
			p := plan.BuildFull(plan.Input{Revision: m.state.Revision, RegistryHost: m.state.RegistryHost, Project: m.state.Project, Images: m.state.Inventory, Cluster: m.state.Cluster, ClusterError: m.state.ClusterError, Archive: m.state.Archive, ArchiveError: m.state.ArchiveError})
			writeImageTable(&b, p.ImageRows, m.width)
			if m.state.Cluster != nil {
				writeWrapped(&b, fmt.Sprintf("Kubernetes %s context %s; namespace %s: %s", m.state.Cluster.KubernetesVersion, m.state.Cluster.Context, m.state.Cluster.Namespace, m.state.Cluster.Preflight.Namespace.Status), m.width, "")
				writeWrapped(&b, "Selected workers: "+strings.Join(m.state.Cluster.Preflight.SelectedWorkers, ", ")+" ("+m.state.Cluster.Preflight.SelectedWorkerStatus.Status+")", m.width, "")
			}
			if m.state.ClusterError != "" {
				writeWrapped(&b, "Kubernetes preflight error: "+m.state.ClusterError, m.width, "")
			}
			if m.state.ArchiveError != "" {
				writeWrapped(&b, "Bundle verification error: "+m.state.ArchiveError, m.width, "")
			}
		}
	case 12:
		p := plan.BuildFull(plan.Input{Revision: m.state.Revision, RegistryHost: m.state.RegistryHost, Project: m.state.Project, Images: m.state.Inventory, Cluster: m.state.Cluster, ClusterError: m.state.ClusterError, Archive: m.state.Archive, ArchiveError: m.state.ArchiveError})
		fmt.Fprintf(&b, "NON-MUTATING REVIEW PLAN | revision %d\n", p.Revision)
		writeWrapped(&b, fmt.Sprintf("Candidate: WSO2 API Manager %s (JDK %s), Keycloak %s (container JDK %s), Oracle %s", p.Profile.WSO2, p.Profile.WSO2JDK, p.Profile.Keycloak, p.Profile.KeycloakJDK, p.Profile.Oracle), m.width, "")
		writeWrapped(&b, "Profile: "+p.Profile.ID+" v"+p.Profile.Version+"; qualification pending", m.width, "")
		writeWrapped(&b, "Registry: "+p.Registry+" / "+p.Project, m.width, "")
		if p.Cluster != nil {
			writeWrapped(&b, fmt.Sprintf("Cluster %s | context %s | API %s | Kubernetes %s | namespace %s", p.Cluster.Cluster, p.Cluster.Context, p.Cluster.APIEndpoint, p.Cluster.KubernetesVersion, p.Cluster.Namespace), m.width, "")
			for _, n := range p.Cluster.Nodes {
				rem := cluster.Remaining(n.Allocatable, n.ScheduledRequests)
				writeWrapped(&b, fmt.Sprintf("Worker/node %s: worker=%t ready=%t eligible=%t %s/%s allocatable %s CPU/%s memory remaining %s CPU/%s memory labels=%v taints=%v", n.Name, n.Worker, n.Ready, n.Eligible, n.OS, n.Architecture, n.Allocatable["cpu"], n.Allocatable["memory"], rem["cpu"], rem["memory"], n.Labels, n.Taints), m.width, "  ")
			}
			for _, perm := range p.Cluster.Preflight.Permissions {
				writeWrapped(&b, fmt.Sprintf("Permission %s %s/%s ns=%s: %s (%s)", perm.Verb, perm.Group, perm.Resource, perm.Namespace, perm.Status, perm.Reason), m.width, "  ")
			}
			writeWrapped(&b, "StorageClass: "+p.Cluster.Preflight.StorageClass.Name+" "+p.Cluster.Preflight.StorageClass.Status+" / "+p.Cluster.Preflight.StorageClass.Deferred, m.width, "")
			writeWrapped(&b, fmt.Sprintf("Storage metadata: provisioner=%s binding=%s reclaim=%s topologies=%s", p.Cluster.Preflight.StorageClass.Provisioner, p.Cluster.Preflight.StorageClass.VolumeBindingMode, p.Cluster.Preflight.StorageClass.ReclaimPolicy, strings.Join(p.Cluster.Preflight.StorageClass.AllowedTopologies, ",")), m.width, "  ")
			writeWrapped(&b, "NodePort range: "+p.Cluster.Preflight.NodePortRange, m.width, "")
			for _, np := range p.Cluster.Preflight.NodePorts {
				writeWrapped(&b, fmt.Sprintf("NodePort %d: %s (%s)", np.Port, np.Status, np.Detail), m.width, "  ")
			}
			writeWrapped(&b, "One-worker-loss assessment: "+p.Capacity.Status+". "+p.Capacity.Assumptions+" "+p.Capacity.Limitations, m.width, "")
			for _, sc := range p.Capacity.Scenarios {
				writeWrapped(&b, fmt.Sprintf("Worker loss %s: %s - %s", sc.FailedWorker, sc.Status, sc.Detail), m.width, "  ")
			}
		} else if m.state.ClusterError != "" {
			writeWrapped(&b, "Kubernetes preflight: "+m.state.ClusterError, m.width, "")
		}
		b.WriteString("\nCandidate component requests and NodePorts:\n")
		for _, c := range p.Profile.Components {
			writeWrapped(&b, fmt.Sprintf("%s: %d replicas, CPU %s, memory %s/%s", c.Name, c.Replicas, c.CPURequest, c.MemoryRequest, c.MemoryLimit), m.width, "  ")
		}
		for _, np := range p.Profile.NodePorts {
			writeWrapped(&b, fmt.Sprintf("%s: %d -> %d", np.Name, np.Port, np.TargetPort), m.width, "  ")
		}
		b.WriteString("\nProposed operation order (review only):\n")
		for _, op := range p.Operations {
			writeWrapped(&b, op, m.width, "  ")
		}
		b.WriteString("\nImage inventory:\n")
		for _, row := range p.ImageRows {
			writeWrapped(&b, fmt.Sprintf("%s | %s -> %s", row.Component, row.SourceReference, row.DestinationReference), m.width, "  ")
			writeWrapped(&b, "Remote "+row.RemoteAvailability+" | local "+row.LocalAvailability+" | integrity "+row.Integrity+" | platform "+row.PlatformCompatibility, m.width, "    ")
			writeWrapped(&b, "Config "+row.ImageConfigDigest+" | OCI manifest "+row.OCIManifestDigest+" | OCI index "+row.OCIIndexDigest+" | archive SHA-256 "+row.ArchiveSHA256, m.width, "    ")
			if row.DockerArchiveManifestSHA256 != "" {
				writeWrapped(&b, "Docker archive metadata SHA-256 "+row.DockerArchiveManifestSHA256, m.width, "    ")
			}
			for _, d := range row.LayerBlobDigests {
				writeWrapped(&b, "Layer blob digest "+d, m.width, "    ")
			}
			for _, d := range row.DiffIDs {
				writeWrapped(&b, "Uncompressed DiffID "+d, m.width, "    ")
			}
			writeWrapped(&b, "Proposed action: "+row.ProposedAction, m.width, "    ")
		}
		for _, section := range []struct {
			name  string
			items []string
		}{{"Required image imports (planning only)", p.Imports}, {"Passed checks", p.Passed}, {"Blockers", p.Blockers}, {"Deferred / unverified", p.Deferred}, {"Warnings and limitations", p.Warnings}} {
			b.WriteString("\n" + section.name + ":\n")
			for _, item := range section.items {
				writeWrapped(&b, item, m.width, "  - ")
			}
		}
		b.WriteString("\nNo changes are authorized or performed by this review. Apply is unavailable.\n")
	default:
		if m.step >= preparationFirstStep && m.step <= preparationLastField {
			field, _ := m.preparationField(m.step)
			writeWrapped(&b, field.prompt, m.width, "")
			if field.secret {
				b.WriteByte('\n')
				writeWrapped(&b, field.label+": "+strings.Repeat("*", len([]rune(m.input))), m.width, "")
			} else {
				b.WriteByte('\n')
				writeWrapped(&b, field.label+": "+m.input, m.width, "")
			}
			if field.key == "external_cert" || field.key == "internal_cert" {
				writeWrapped(&b, "Use | between the four paths; the chain entry can be empty (for example cert.pem|key.pem||trust.pem).", m.width, "")
			}
		} else if m.step == preparationReviewStep {
			b.WriteString("STAGE 3 LOCAL PREPARATION REVIEW\n\n")
			if m.preparing {
				b.WriteString("RUNNING: validating local inputs and generating the redacted export. Press Esc to cancel.\n")
			} else if report := m.state.PreparationReport; report != nil {
				fmt.Fprintf(&b, "File generation: %s | Deployment readiness: %s\n", report.FileGenerationStatus, report.DeploymentReadiness)
				writeWrapped(&b, "Public export: "+m.state.OutputDirectory, m.width, "")
				writeWrapped(&b, "Protected state: "+m.state.StateDirectory, m.width, "")
				fmt.Fprintf(&b, "Oracle check: %s (read-only: %t)\n", report.Oracle.Status, report.Oracle.ReadOnly)
				for _, check := range report.Oracle.Checks {
					writeWrapped(&b, "Oracle "+check.Account+": "+check.Status+" - "+check.Detail, m.width, "  ")
				}
				if report.SchemaArtifacts != nil {
					writeWrapped(&b, fmt.Sprintf("Schema artifacts: %s for %s %s; source %s SHA-256 %s; %d script(s)", report.SchemaArtifacts.Status, report.SchemaArtifacts.Product, report.SchemaArtifacts.Version, report.SchemaArtifacts.SourceIdentity, report.SchemaArtifacts.SourceSHA256, len(report.SchemaArtifacts.Scripts)), m.width, "")
					for _, script := range report.SchemaArtifacts.Scripts {
						writeWrapped(&b, fmt.Sprintf("Schema script %s -> %s (order %d, SHA-256 %s)", script.Path, script.TargetSchema, script.Order, script.SHA256), m.width, "  ")
					}
					for _, item := range report.SchemaArtifacts.Blockers {
						writeWrapped(&b, "Schema artifact blocker: "+item, m.width, "  - ")
					}
					for _, item := range report.SchemaArtifacts.Deferred {
						writeWrapped(&b, "Schema artifact deferred: "+item, m.width, "  - ")
					}
				} else {
					b.WriteString("Schema artifacts: not supplied or not required by prepared mode.\n")
				}
				fmt.Fprintf(&b, "Certificates: %d service certificate(s); root %s\n", len(report.Certificates.Certificates), report.Certificates.Root.Subject)
				for _, cert := range report.Certificates.Certificates {
					writeWrapped(&b, fmt.Sprintf("Certificate %s SANs=%s expires=%s trust=%s", cert.Purpose, strings.Join(cert.SANs, ","), cert.NotAfter, cert.Trust), m.width, "  ")
				}
				b.WriteString("Generated public files and SHA-256:\n")
				fileNames := make([]string, 0, len(report.Files))
				for name := range report.Files {
					fileNames = append(fileNames, name)
				}
				sort.Strings(fileNames)
				for _, name := range fileNames {
					writeWrapped(&b, name+"  "+report.Files[name], m.width, "  ")
				}
				b.WriteString("Blockers:\n")
				if len(report.Blockers) == 0 {
					b.WriteString("  None\n")
				}
				for _, item := range report.Blockers {
					writeWrapped(&b, item, m.width, "  - ")
				}
				b.WriteString("Deferred checks:\n")
				if len(report.Deferred) == 0 {
					b.WriteString("  None\n")
				}
				for _, item := range report.Deferred {
					writeWrapped(&b, item, m.width, "  - ")
				}
			} else {
				cfg := m.preparationConfig()
				fmt.Fprintf(&b, "Oracle target: %s host=%s port=%d service=%s descriptor=%s\n", m.state.OracleTargetKind, cfg.Oracle.Host, cfg.Oracle.Port, cfg.Oracle.ServiceName, cfg.Oracle.ConnectDescriptor)
				writeWrapped(&b, fmt.Sprintf("Oracle transport=%s DN=%s wallet=%s trust=%s timeouts=%ds/%ds", cfg.Oracle.ConnectionMode, cfg.Oracle.ServerCertificateDN, cfg.Oracle.WalletPath, cfg.Oracle.TrustBundlePath, cfg.Oracle.ConnectTimeoutSeconds, cfg.Oracle.ReadTimeoutSeconds), m.width, "")
				writeWrapped(&b, fmt.Sprintf("Schema mode=%s schemas=%s,%s,%s runtime accounts=%s,%s,%s owner accounts=%s,%s,%s", cfg.Oracle.SchemaMode, cfg.Oracle.APIMSchema, cfg.Oracle.SharedSchema, cfg.Oracle.KeycloakSchema, cfg.Oracle.APIMRuntimeAccount, cfg.Oracle.SharedRuntimeAccount, cfg.Oracle.KeycloakRuntimeAccount, cfg.Oracle.APIMOwnerAccount, cfg.Oracle.SharedOwnerAccount, cfg.Oracle.KeycloakOwnerAccount), m.width, "")
				oracleAuthStatus := "deferred"
				if m.state.CheckOracle {
					oracleAuthStatus = "requested; credentials are session-only and masked"
				}
				writeWrapped(&b, "Authenticated Oracle validation: "+oracleAuthStatus, m.width, "")
				fmt.Fprintf(&b, "Certificates: %s | %s, %s, %s | cluster DNS %s\n", cfg.Certificates.Mode, cfg.Certificates.APIMHostname, cfg.Certificates.GatewayHostname, cfg.Certificates.KeycloakHostname, cfg.Deployment.ClusterDNSDomain)
				if cfg.Certificates.Mode == "import" {
					writeWrapped(&b, fmt.Sprintf("External cert/key/chain/trust: %s | %s | %s | %s", cfg.Certificates.ExternalCertificatePath, cfg.Certificates.ExternalPrivateKeyPath, cfg.Certificates.ExternalChainPath, cfg.Certificates.ExternalTrustPath), m.width, "")
					writeWrapped(&b, fmt.Sprintf("Internal cert/key/chain/trust: %s | %s | %s | %s", cfg.Certificates.InternalCertificatePath, cfg.Certificates.InternalPrivateKeyPath, cfg.Certificates.InternalChainPath, cfg.Certificates.InternalTrustPath), m.width, "")
				}
				writeWrapped(&b, "WSO2 distribution: "+cfg.Artifacts.WSO2DistributionPath, m.width, "")
				writeWrapped(&b, "WSO2 distribution SHA-256: "+cfg.Artifacts.WSO2DistributionSHA256, m.width, "")
				writeWrapped(&b, "Schema manifest: "+cfg.Artifacts.SchemaManifestPath, m.width, "")
				writeWrapped(&b, fmt.Sprintf("Distributed throttle: %s:%d type=%s replicated=%t TLS=%t trust=%s", cfg.DistributedThrottle.Host, cfg.DistributedThrottle.Port, cfg.DistributedThrottle.Type, cfg.DistributedThrottle.ReplicationMode, cfg.DistributedThrottle.TLS, cfg.DistributedThrottle.TrustBundlePath), m.width, "")
				writeWrapped(&b, "Public output: "+m.state.OutputDirectory, m.width, "")
				writeWrapped(&b, "Protected state: "+m.state.StateDirectory, m.width, "")
				b.WriteString("No preparation export exists for this revision. Press e to run local preparation.\n")
			}
			writeWrapped(&b, "Preparation only. This does not run DDL or apply cluster/identity resources.", m.width, "")
		}
	}
	if m.errText != "" {
		b.WriteString("\nStatus: " + m.errText + "\n")
	}
	if m.checking {
		b.WriteString("\nStatus: RUNNING - registry manifest checks are cancellable with Esc.\n")
	}
	if m.preparing {
		b.WriteString("\nStatus: RUNNING - local preparation is cancellable with Esc.\n")
	}
	writeWrapped(&b, "Enter: continue/check/review | e: generate Stage 3 export on its review step | Esc: back/cancel | Ctrl-E: edit fields | r: retry inventory | PageUp/PageDown: scroll | F1: help | Ctrl-C: cancel", m.width, "")
	writeWrapped(&b, "Log: "+m.logPath, m.width, "")
	b.WriteString(domain.Attribution + "\n")
	v := tea.NewView(fitContent(b.String(), m.height, m.scroll))
	return v
}

func writeImageTable(b *strings.Builder, rows []plan.ImageRow, width int) {
	usable := min(max(width-4, 20), 116)
	if width > 0 && usable < 68 {
		b.WriteString("IMAGE STATUS (compact layout)\n")
		for _, row := range rows {
			writeWrapped(b, row.Component+" | remote "+shortRemote(row.RemoteAvailability)+" | local "+shortLocal(row.LocalAvailability)+" | integrity "+row.Integrity+" | platform "+shortPlatform(row.PlatformCompatibility), width, "  ")
			writeWrapped(b, "Action: "+row.ProposedAction, width, "    ")
		}
		return
	}
	componentWidth, remoteWidth, localWidth, integrityWidth := 23, 10, 10, 9
	platformWidth := usable - componentWidth - remoteWidth - localWidth - integrityWidth - 4
	if platformWidth < 12 {
		platformWidth = 12
	}
	fmt.Fprintf(b, "%-*s %-*s %-*s %-*s %-*s\n", componentWidth, "COMPONENT", remoteWidth, "REMOTE", localWidth, "LOCAL", integrityWidth, "INTEGRITY", platformWidth, "PLATFORM")
	b.WriteString(strings.Repeat("-", min(usable, componentWidth+remoteWidth+localWidth+integrityWidth+platformWidth+4)) + "\n")
	for _, row := range rows {
		fmt.Fprintf(b, "%-*s %-*s %-*s %-*s %-*s\n", componentWidth, clipCell(row.Component, componentWidth), remoteWidth, clipCell(shortRemote(row.RemoteAvailability), remoteWidth), localWidth, clipCell(shortLocal(row.LocalAvailability), localWidth), integrityWidth, clipCell(row.Integrity, integrityWidth), platformWidth, clipCell(shortPlatform(row.PlatformCompatibility), platformWidth))
		writeWrapped(b, "Source: "+row.SourceReference, width, "  ")
		writeWrapped(b, "Destination: "+row.DestinationReference, width, "  ")
		writeWrapped(b, "Action: "+row.ProposedAction, width, "  ")
	}
}

func shortRemote(status string) string {
	if strings.HasPrefix(status, "MISSING") {
		return "MISSING"
	}
	return status
}

func shortLocal(status string) string {
	switch {
	case strings.HasPrefix(status, "PRESENT"):
		return "PRESENT"
	case strings.HasPrefix(status, "INVALID"):
		return "INVALID"
	default:
		return "ABSENT"
	}
}

func shortPlatform(status string) string {
	switch {
	case strings.Contains(status, "INCOMPATIBLE"), strings.Contains(status, "INCOMPLETE"):
		return "INCOMPLETE"
	case strings.Contains(status, "COMPATIBLE"):
		return "COMPATIBLE"
	case strings.Contains(status, "UNVERIFIED"):
		return "UNVERIFIED"
	default:
		return "NOT CHECKED"
	}
}

func clipCell(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}

func fitContent(content string, height, offset int) string {
	if height <= 0 {
		return content
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) <= height {
		return content
	}
	headerEnd := 0
	for i, line := range lines {
		if len(line) >= 12 && strings.Trim(line, "-") == "" {
			headerEnd = i + 1
			break
		}
	}
	footerStart := len(lines) - 1
	for i, line := range lines {
		if strings.HasPrefix(line, "Enter:") {
			footerStart = i
			break
		}
	}
	if headerEnd == 0 || footerStart <= headerEnd {
		return strings.Join(lines[:height], "\n")
	}
	header := lines[:headerEnd]
	footer := lines[footerStart:]
	body := lines[headerEnd:footerStart]
	visible := height - len(header) - len(footer)
	if visible < 1 {
		visible = 1
	}
	maxOffset := max(0, len(body)-visible)
	offset = min(max(0, offset), maxOffset)
	end := min(len(body), offset+visible)
	view := make([]string, 0, height)
	view = append(view, header...)
	view = append(view, body[offset:end]...)
	view = append(view, footer...)
	if len(view) > height {
		view = view[:height]
	}
	return strings.Join(view, "\n")
}

func writeWrapped(b *strings.Builder, text string, width int, indent string) {
	if width <= 0 {
		width = 80
	}
	available := max(8, width-len([]rune(indent)))
	words := strings.Fields(text)
	if len(words) == 0 {
		b.WriteString(indent + "\n")
		return
	}
	line := indent
	for _, word := range words {
		for len([]rune(word)) > available {
			r := []rune(word)
			chunk := string(r[:available])
			if line != indent {
				b.WriteString(line + "\n")
				line = indent
			}
			b.WriteString(line + chunk + "\n")
			line = indent
			word = string(r[available:])
		}
		if len([]rune(line))+1+len([]rune(word)) > width && line != indent {
			b.WriteString(line + "\n")
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += word
	}
	b.WriteString(line + "\n")
}

func Run(ctx context.Context, m *Model) error {
	m.ctx = ctx
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}
