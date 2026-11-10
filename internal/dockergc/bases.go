package dockergc

import (
	"bufio"
	"bytes"
	"context"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const kindNodeRepo = "kindest/node"

func ParseFromLines(dockerfile []byte) []string {
	var out []string
	stages := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(dockerfile))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		args := fields[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			args = args[1:]
		}
		if len(args) == 0 {
			continue
		}
		from := args[0]
		if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
			stages[strings.ToLower(args[2])] = true
		}
		if strings.Contains(from, "$") || strings.EqualFold(from, "scratch") || stages[strings.ToLower(from)] {
			continue
		}
		out = append(out, from)
	}
	return out
}

type BaseOptions struct {
	From []string
	// Set only when no kind cluster is running.
	Kind bool
}

func PlanStaleBases(ctx context.Context, api API, opts BaseOptions) ([]ImageItem, error) {
	containers, err := api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, apperr.Wrap(err, "dockergc.errors.list_containers")
	}
	images, err := api.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, apperr.Wrap(err, "dockergc.errors.list_images")
	}
	return staleBases(images, containers, opts), nil
}

func staleBases(images []image.Summary, containers []types.Container, opts BaseOptions) []ImageItem {
	used := map[string]bool{}
	for i := range containers {
		used[containers[i].ImageID] = true
	}
	pins := map[string][]ref{}
	for _, f := range opts.From {
		r := splitRef(normalizeRef(f))
		pins[r.repo] = append(pins[r.repo], r)
	}
	var out []ImageItem
	for i := range images {
		img := &images[i]
		if used[img.ID] || isPinned(img, pins) {
			continue
		}
		refs := staleRefs(img, pins, opts.Kind)
		untaggedKind := opts.Kind && len(tagRefs(img)) == 0 && hasRepoDigest(img, kindNodeRepo)
		if len(refs) == 0 && !untaggedKind {
			continue
		}
		reason := i18n.Text("dockergc.reasons.stale_base")
		if untaggedKind || (len(refs) > 0 && splitRef(normalizeRef(refs[0])).repo == kindNodeRepo) {
			reason = i18n.Text("dockergc.reasons.kind_node")
		}
		out = append(out, ImageItem{
			ID: img.ID, Refs: refs, Size: img.Size, Kind: ImageBase, Reason: reason,
			Created: time.Unix(img.Created, 0), Shared: len(refs) > 0 && len(refs) < len(tagRefs(img)),
		})
	}
	sortImages(out)
	return out
}

func staleRefs(img *image.Summary, pins map[string][]ref, kind bool) []string {
	var refs []string
	for _, t := range tagRefs(img) {
		r := splitRef(normalizeRef(t))
		if (kind && r.repo == kindNodeRepo) || staleTag(r, pins[r.repo]) {
			refs = append(refs, t)
		}
	}
	return refs
}

func isPinned(img *image.Summary, pins map[string][]ref) bool {
	for _, s := range append(tagRefs(img), img.RepoDigests...) {
		r := splitRef(normalizeRef(s))
		for _, pin := range pins[r.repo] {
			if pin.digest != "" && (r.digest == pin.digest || img.ID == pin.digest) {
				return true
			}
			if pin.digest == "" && r.tag != "" && r.tag == pin.tag {
				return true
			}
		}
	}
	return false
}

func staleTag(r ref, pins []ref) bool {
	if r.tag == "" {
		return false
	}
	for _, pin := range pins {
		if pin.tag != "" && tagFamily(pin.tag) == tagFamily(r.tag) {
			return true
		}
	}
	return false
}

func tagFamily(tag string) string {
	var b strings.Builder
	numbers := 0
	for i := 0; i < len(tag); {
		c := tag[i]
		if c < '0' || c > '9' {
			b.WriteByte(c)
			i++
			continue
		}
		j := i
		for j < len(tag) && (tag[j] == '.' || (tag[j] >= '0' && tag[j] <= '9')) {
			j++
		}
		run := tag[i:j]
		if numbers == 0 {
			major, _, _ := strings.Cut(run, ".")
			b.WriteString(major)
		} else {
			b.WriteByte('#')
		}
		numbers++
		i = j
	}
	return b.String()
}

func hasRepoDigest(img *image.Summary, repo string) bool {
	for _, d := range img.RepoDigests {
		if splitRef(normalizeRef(d)).repo == repo {
			return true
		}
	}
	return false
}

func SortedUnique(refs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}
