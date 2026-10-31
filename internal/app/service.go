package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/auditlog"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

type Service struct {
	Registry *registry.Client
	Log      *auditlog.Logger
}

func New(r *registry.Client, l *auditlog.Logger) *Service { return &Service{Registry: r, Log: l} }

func (s *Service) CheckImages(ctx context.Context, host, project string, c registry.Credentials, revision uint64, targetPlatforms ...[]registry.Platform) ([]registry.Image, error) {
	if c.CABundlePath == "" {
		c.CABundlePath = os.Getenv("SYSDEV_REGISTRY_CA_BUNDLE")
	}
	start := time.Now()
	s.Log.Event("INFO", "registry-validation", "Checking exact required image references", map[string]string{"registry": host, "project": project, "revision": fmt.Sprint(revision)})
	if err := s.Log.Error(); err != nil {
		return nil, fmt.Errorf("required audit log is unavailable; stopping before registry checks: %w", err)
	}
	var images []registry.Image
	var err error
	if len(targetPlatforms) > 0 {
		images, err = s.Registry.CheckForPlatforms(ctx, host, project, c, registry.Profile.Images, targetPlatforms[0])
	} else {
		images, err = s.Registry.Check(ctx, host, project, c, registry.Profile.Images)
	}
	for _, im := range images {
		s.Log.Event("INFO", "registry-image", string(im.Status), map[string]string{"component": im.Component, "reference": im.Reference, "digest": im.Digest, "media_type": im.MediaType, "platform_status": im.PlatformStatus, "detail": im.Detail})
	}
	fields := map[string]string{"duration": time.Since(start).String(), "revision": fmt.Sprint(revision)}
	if err != nil {
		fields["error"] = err.Error()
		s.Log.Event("ERROR", "registry-validation", "Registry inventory did not complete", fields)
	} else {
		s.Log.Event("INFO", "registry-validation", "Registry inventory completed", fields)
	}
	return images, err
}
