// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type baseImage struct {
	// repository@sha256:..., of the index when the base is multi-platform: BuildKit picks its node's
	// platform from it.
	pinned string
	digest string
	labels map[string]string
}

type imageRegistry interface {
	base(ctx context.Context, ref string, kc authn.Keychain) (baseImage, error)
	exists(ctx context.Context, ref string, kc authn.Keychain) (bool, error)
}

type ggcrRegistry struct {
	// The bundled registry, reached over plain HTTP.
	insecureHost string
}

func (g ggcrRegistry) parse(ref string) (name.Reference, error) {
	var opts []name.Option
	if g.insecureHost != "" && strings.HasPrefix(ref, g.insecureHost+"/") {
		opts = append(opts, name.Insecure)
	}
	r, err := name.ParseReference(ref, opts...)
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.image_build.registry_read", "ref", ref)
	}
	return r, nil
}

func (g ggcrRegistry) base(ctx context.Context, ref string, kc authn.Keychain) (baseImage, error) {
	r, err := g.parse(ref)
	if err != nil {
		return baseImage{}, err
	}
	fail := func(err error) (baseImage, error) {
		return baseImage{}, apperr.Wrap(err, "kuberuntime.image_build.registry_read", "ref", ref)
	}
	desc, err := remote.Get(r, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: runtime.GOARCH}))
	if err != nil {
		return fail(err)
	}
	img, err := desc.Image()
	if err != nil {
		return fail(err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return fail(err)
	}
	return baseImage{
		pinned: r.Context().Name() + "@" + desc.Digest.String(),
		digest: desc.Digest.String(),
		labels: cfg.Config.Labels,
	}, nil
}

func (g ggcrRegistry) exists(ctx context.Context, ref string, kc authn.Keychain) (bool, error) {
	r, err := g.parse(ref)
	if err != nil {
		return false, err
	}
	_, err = remote.Head(r, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc))
	var terr *transport.Error
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound:
		return false, nil
	default:
		return false, apperr.Wrap(err, "kuberuntime.image_build.registry_read", "ref", ref)
	}
}

// dockerConfig answers for the registries a kubernetes.io/dockerconfigjson Secret lists and leaves
// the rest anonymous.
type dockerConfig struct {
	Auths map[string]struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Auth     string `json:"auth"`
	} `json:"auths"`
}

func (d dockerConfig) Resolve(res authn.Resource) (authn.Authenticator, error) {
	for host, a := range d.Auths {
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		host, _, _ = strings.Cut(host, "/")
		if host != res.RegistryStr() {
			continue
		}
		user, pass := a.Username, a.Password
		if user == "" && a.Auth != "" {
			if raw, err := base64.StdEncoding.DecodeString(a.Auth); err == nil {
				user, pass, _ = strings.Cut(string(raw), ":")
			}
		}
		return authn.FromConfig(authn.AuthConfig{Username: user, Password: pass}), nil
	}
	return authn.Anonymous, nil
}

func parseDockerConfig(raw []byte) (dockerConfig, error) {
	var d dockerConfig
	if err := json.Unmarshal(raw, &d); err != nil {
		return dockerConfig{}, err
	}
	return d, nil
}

func (b *imageBuilder) credentials(ctx context.Context) (authn.Keychain, error) {
	if b.cfg.AuthSecret == "" {
		return authn.DefaultKeychain, nil
	}
	sec, err := b.rt.client.CoreV1().Secrets(b.cfg.Namespace).Get(ctx, b.cfg.AuthSecret, metav1.GetOptions{})
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.image_build.auth_secret", "ns", b.cfg.Namespace, "name", b.cfg.AuthSecret)
	}
	d, err := parseDockerConfig(sec.Data[corev1.DockerConfigJsonKey])
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.image_build.auth_secret", "ns", b.cfg.Namespace, "name", b.cfg.AuthSecret)
	}
	return d, nil
}
