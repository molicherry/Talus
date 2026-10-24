package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/vpsmanager/backend/internal/agentproto"
	"gorm.io/gorm"
)

var errAgentDeploymentPending = errors.New("agent deployment pending")

type trustedAgentArtifacts interface {
	Find(context.Context, string, string) (agentproto.Artifact, error)
}

func (s *AgentDeploymentService) SetTrustedArtifacts(artifacts trustedAgentArtifacts) {
	s.trustedArtifacts = artifacts
}

// CollectInstalled never uploads or waits for a deployment. A known, compatible
// older artifact can keep serving while a separate lease installs the new one.
func (s *AgentDeploymentService) CollectInstalled(ctx context.Context, lease Lease) (*agentOutput, bool, error) {
	platform, err := s.Probe(ctx, lease)
	if err != nil {
		return nil, false, err
	}
	if platform.OS != "linux" {
		return nil, false, deployError(ReasonAgentPlatformUnsupported, errors.New("resident monitoring agent required on Windows"))
	}
	desired, err := s.artifactFor(platform)
	if err != nil {
		return nil, false, err
	}
	digest, err := s.remoteHash(ctx, lease, agentPath)
	if err != nil {
		return nil, false, err
	}
	upgrade := digest != desired.SHA256
	installed, err := s.installedArtifact(ctx, desired, digest)
	if err != nil {
		return nil, upgrade, err
	}
	out, err := s.runAgent(ctx, lease)
	if err != nil {
		return nil, upgrade, err
	}
	err = verifyProtocol(out, installed, desired.ProtocolVersion)
	if err != nil && out != nil && out.AgentVersion == desired.BuildVersion && upgrade {
		// Atomic activation can occur between hashing the old path and exec.
		// Accept the new report only after independently verifying the new hash.
		current, hashErr := s.remoteHash(ctx, lease, agentPath)
		if hashErr == nil && current == desired.SHA256 {
			err = verifyProtocol(out, desired, desired.ProtocolVersion)
			upgrade = false
		}
	}
	if err != nil {
		return nil, upgrade, err
	}
	return out, upgrade, nil
}

func (s *AgentDeploymentService) installedArtifact(ctx context.Context, desired agentproto.Artifact, digest string) (agentproto.Artifact, error) {
	if digest == desired.SHA256 {
		return desired, nil
	}
	if digest == "" || s.trustedArtifacts == nil {
		return agentproto.Artifact{}, errAgentDeploymentPending
	}
	installed, err := s.trustedArtifacts.Find(ctx, desired.Platform, digest)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agentproto.Artifact{}, errAgentDeploymentPending
	}
	if err != nil {
		return agentproto.Artifact{}, fmt.Errorf("find trusted agent artifact: %w", err)
	}
	if installed.Platform != desired.Platform || installed.SHA256 != digest || installed.BuildVersion == "" || installed.ProtocolVersion != desired.ProtocolVersion {
		return agentproto.Artifact{}, errAgentDeploymentPending
	}
	return installed, nil
}
