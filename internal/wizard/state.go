package wizard

import (
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/cluster"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/imagearchive"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

type State struct {
	RegistryHost, Project, AuthMode, Username string
	Password, Token                           string
	KubeconfigPath, ContextName, Namespace    string
	WorkerNames, StorageClass, BundlePath     string
	Revision                                  uint64
	Inventory                                 []registry.Image
	InventoryRevision                         uint64
	InventoryStatus                           string
	Cluster                                   *cluster.Inventory
	Archive                                   *imagearchive.Result
	ArchiveError                              string
	ClusterError                              string
}

func (s *State) Set(field, value string) {
	changed := false
	switch field {
	case "registry":
		changed = s.RegistryHost != value
		s.RegistryHost = value
	case "project":
		changed = s.Project != value
		s.Project = value
	case "auth":
		changed = s.AuthMode != value
		s.AuthMode = value
		if changed {
			s.Username = ""
			s.Password = ""
			s.Token = ""
		}
	case "username":
		changed = s.Username != value
		s.Username = value
	case "password":
		changed = s.Password != value
		s.Password = value
	case "token":
		changed = s.Token != value
		s.Token = value
	case "kubeconfig":
		changed = s.KubeconfigPath != value
		s.KubeconfigPath = value
	case "context":
		changed = s.ContextName != value
		s.ContextName = value
	case "namespace":
		changed = s.Namespace != value
		s.Namespace = value
	case "workers":
		changed = s.WorkerNames != value
		s.WorkerNames = value
	case "storage":
		changed = s.StorageClass != value
		s.StorageClass = value
	case "bundle":
		changed = s.BundlePath != value
		s.BundlePath = value
	}
	if changed {
		s.Revision++
		switch field {
		case "kubeconfig", "context", "namespace", "workers", "storage":
			s.Inventory = nil
			s.InventoryRevision = 0
			s.InventoryStatus = "STALE"
			s.Cluster = nil
			s.ClusterError = ""
		case "bundle":
			s.Archive = nil
			s.ArchiveError = ""
			s.InventoryStatus = "STALE"
			s.InventoryRevision = 0
		default:
			s.Inventory = nil
			s.InventoryRevision = 0
			s.InventoryStatus = "STALE"
		}
	}
}

func (s State) Credentials() registry.Credentials {
	switch s.AuthMode {
	case "basic":
		return registry.Credentials{Username: s.Username, Password: s.Password}
	case "token":
		return registry.Credentials{Token: s.Token}
	}
	return registry.Credentials{}
}
