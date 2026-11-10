package dockergc

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/agentnet"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

type API interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
	ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
	ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error)
	VolumeList(ctx context.Context, options volume.ListOptions) (volume.ListResponse, error)
	VolumeRemove(ctx context.Context, volumeID string, force bool) error
	NetworkList(ctx context.Context, options network.ListOptions) ([]network.Summary, error)
	NetworkRemove(ctx context.Context, networkID string) error
}

type Options struct {
	Keep          []string
	Rollback      []string
	RollbackUntil time.Time
	// An image newer than Running is kept: an update pulls it before the configuration naming it
	// reaches this process, so the running daemon must not remove it mid-pull.
	Running         string
	ComposeProjects []string
	Registries      []string
	AgentNetwork    string
	ContainerMinAge time.Duration
	// Set only when Keep is complete.
	PruneUnclaimed bool
	ImagesOnly     bool
	Uninstall      bool
	PurgeData      bool
	Now            time.Time
}

type ContainerItem struct {
	ID      string
	Name    string
	Image   string
	State   string
	Kind    ContainerKind
	running bool
}

type ImageItem struct {
	ID      string
	Refs    []string
	Size    int64
	Kind    ImageKind
	Reason  string
	Created time.Time
	Shared  bool
}

func (i ImageItem) Name() string {
	if len(i.Refs) > 0 {
		return i.Refs[0]
	}
	return shortID(i.ID)
}

type VolumeItem struct {
	Name   string
	Reason string
}

type NetworkItem struct {
	ID   string
	Name string
}

type Plan struct {
	Containers []ContainerItem
	Images     []ImageItem
	Volumes    []VolumeItem
	Networks   []NetworkItem
	Kept       []ImageItem
}

func (p *Plan) Empty() bool {
	return len(p.Containers)+len(p.Images)+len(p.Volumes)+len(p.Networks) == 0
}

func (p *Plan) ImageBytes() int64 {
	var n int64
	for i := range p.Images {
		if !p.Images[i].Shared {
			n += p.Images[i].Size
		}
	}
	return n
}

func Scan(ctx context.Context, api API, opts Options) (*Plan, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.AgentNetwork == "" {
		opts.AgentNetwork = agentnet.DefaultNetwork
	}
	containers, err := api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, apperr.Wrap(err, "dockergc.errors.list_containers")
	}
	images, err := api.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, apperr.Wrap(err, "dockergc.errors.list_images")
	}
	p := &planner{opts: &opts, cls: newClassifier(&opts), api: api, plan: &Plan{}}
	p.indexImages(images)
	if err := p.planContainers(ctx, containers); err != nil {
		return nil, err
	}
	p.planImages(images, containers)
	if opts.ImagesOnly {
		return p.plan, nil
	}
	if err := p.planVolumes(ctx, containers); err != nil {
		return nil, err
	}
	if err := p.planNetworks(ctx, containers); err != nil {
		return nil, err
	}
	return p.plan, nil
}

type planner struct {
	opts *Options
	cls  *classifier
	api  API
	plan *Plan

	byID     map[string]*image.Summary
	owned    map[string]bool
	removing map[string]bool
}

func (p *planner) indexImages(images []image.Summary) {
	p.byID = make(map[string]*image.Summary, len(images))
	for i := range images {
		p.byID[images[i].ID] = &images[i]
	}
	p.owned = map[string]bool{}
	for i := range images {
		if p.ownedImage(&images[i], 0) {
			p.owned[images[i].ID] = true
		}
	}
}

// The classic builder leaves untagged intermediate images whose only link to the build is their
// parent.
func (p *planner) ownedImage(img *image.Summary, depth int) bool {
	if kind, _, _ := p.cls.image(img); kind != "" {
		return true
	}
	if depth > 64 || len(tagRefs(img)) > 0 || img.ParentID == "" {
		return false
	}
	parent, ok := p.byID[img.ParentID]
	return ok && p.ownedImage(parent, depth+1)
}

func (p *planner) planContainers(ctx context.Context, containers []types.Container) error {
	p.removing = map[string]bool{}
	owned := func(id string) bool { return p.owned[id] }
	for i := range containers {
		c := &containers[i]
		kind := p.cls.container(c, owned)
		if kind == "" || p.opts.ImagesOnly {
			continue
		}
		remove, err := p.removable(ctx, c, kind)
		if err != nil {
			return err
		}
		if !remove {
			continue
		}
		p.removing[c.ID] = true
		p.plan.Containers = append(p.plan.Containers, ContainerItem{
			ID: c.ID, Name: containerName(c), Image: c.Image, State: c.State, Kind: kind,
			running: isRunning(c.State),
		})
	}
	sort.Slice(p.plan.Containers, func(i, j int) bool {
		a, b := p.plan.Containers[i], p.plan.Containers[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return nil
}

func (p *planner) removable(ctx context.Context, c *types.Container, kind ContainerKind) (bool, error) {
	if p.opts.Uninstall {
		return true, nil
	}
	switch kind {
	case ContainerBuild:
		return true, nil
	case ContainerAgent, ContainerSandbox, ContainerChat, ContainerBrowser, ContainerLogin:
		if isRunning(c.State) {
			return false, nil
		}
		return p.oldEnough(ctx, c)
	case ContainerMCP, ContainerStack:
		return false, nil
	}
	return false, nil
}

func (p *planner) oldEnough(ctx context.Context, c *types.Container) (bool, error) {
	if p.opts.ContainerMinAge <= 0 {
		return true, nil
	}
	finished := time.Unix(c.Created, 0)
	info, err := p.api.ContainerInspect(ctx, c.ID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, apperr.Wrap(err, "dockergc.errors.inspect_container", "id", shortID(c.ID))
	}
	if info.ContainerJSONBase != nil && info.State != nil {
		if t, parseErr := time.Parse(time.RFC3339Nano, info.State.FinishedAt); parseErr == nil && t.Year() > 1 {
			finished = t
		}
	}
	return p.opts.Now.Sub(finished) >= p.opts.ContainerMinAge, nil
}

func (p *planner) planImages(images []image.Summary, containers []types.Container) {
	r := p.newRetention(containers)
	type candidate struct {
		img    *image.Summary
		item   ImageItem
		family string
	}
	var candidates []candidate
	for i := range images {
		img := &images[i]
		if !p.owned[img.ID] {
			continue
		}
		item, family := p.describe(img)
		if item.Reason = r.keepReason(img); item.Reason != "" {
			r.claimed[family] = true
			if img.Created > r.newestKept[family] {
				r.newestKept[family] = img.Created
			}
			p.plan.Kept = append(p.plan.Kept, item)
			continue
		}
		candidates = append(candidates, candidate{img: img, item: item, family: family})
	}
	// Second pass: whether a family kept anything is only known once every image is seen.
	for i := range candidates {
		c := &candidates[i]
		var remove bool
		c.item.Reason, remove = r.removeReason(c.img, c.item.Kind, c.family)
		if remove {
			p.plan.Images = append(p.plan.Images, c.item)
		} else {
			p.plan.Kept = append(p.plan.Kept, c.item)
		}
	}
	sortImages(p.plan.Images)
	sortImages(p.plan.Kept)
}

func (p *planner) describe(img *image.Summary) (ImageItem, string) {
	kind, family, refs := p.cls.image(img)
	if kind == "" {
		kind, family = ImageBuild, daemonFamily
	}
	return ImageItem{
		ID: img.ID, Refs: refs, Size: img.Size, Kind: kind,
		Created: time.Unix(img.Created, 0),
		Shared:  len(refs) > 0 && len(refs) < len(tagRefs(img)),
	}, family
}

type retention struct {
	opts         *Options
	inUse        map[string][]string
	keep         map[string]bool
	rollback     map[string]bool
	rollbackLive bool
	claimed      map[string]bool
	// An image built after the newest one kept in its family is a pending update or another install
	// on the
	// same engine (e.g. a second compose stack), never an older version to prune.
	newestKept map[string]int64
}

func (p *planner) newRetention(containers []types.Container) *retention {
	r := &retention{
		opts:     p.opts,
		inUse:    p.imagesInUse(containers),
		keep:     refSet(p.opts.Keep),
		rollback: refSet(p.opts.Rollback),
		claimed:  map[string]bool{},

		newestKept: map[string]int64{},
	}
	r.rollbackLive = len(r.rollback) > 0 && p.opts.Now.Before(p.opts.RollbackUntil)
	for _, ref := range p.opts.Keep {
		if kind, family := p.cls.repo(splitRef(normalizeRef(ref)).repo); kind != "" {
			r.claimed[family] = true
		}
	}
	return r
}

func (r *retention) keepReason(img *image.Summary) string {
	switch {
	case len(r.inUse[img.ID]) > 0:
		return i18n.Text("dockergc.reasons.in_use", "containers", strings.Join(r.inUse[img.ID], ", "))
	case r.opts.Uninstall:
		return ""
	case matches(img, r.keep):
		return i18n.Text("dockergc.reasons.configured")
	case r.rollbackLive && matches(img, r.rollback):
		return i18n.Text("dockergc.reasons.rollback_until", "date", r.opts.RollbackUntil.Format(time.DateOnly))
	}
	if v := imageVersion(img); v != "" && semver.Newer(v, r.opts.Running) {
		return i18n.Text("dockergc.reasons.newer_than_running", "version", r.opts.Running)
	}
	return ""
}

// The version label covers images pulled by digest, which have no tag.
func imageVersion(img *image.Summary) string {
	for _, t := range tagRefs(img) {
		if tag := splitRef(t).tag; tag != "" {
			if _, ok := semver.Parse(tag); ok {
				return tag
			}
		}
	}
	if v := strings.TrimSpace(img.Labels[labelImageVersion]); v != "" {
		if _, ok := semver.Parse(v); ok {
			return v
		}
	}
	return ""
}

func (r *retention) removeReason(img *image.Summary, kind ImageKind, family string) (reason string, remove bool) {
	switch {
	case r.opts.Uninstall:
		return i18n.Text("dockergc.reasons.uninstall"), true
	case kind == ImageBuild:
		return i18n.Text("dockergc.reasons.untagged_build"), true
	case r.claimed[family] && matches(img, r.rollback):
		return i18n.Text("dockergc.reasons.rollback_ended", "date", r.opts.RollbackUntil.Format(time.DateOnly)), true
	case r.claimed[family] && r.newestKept[family] != 0 && img.Created > r.newestKept[family]:
		return i18n.Text("dockergc.reasons.newer_than_kept"), false
	case r.claimed[family]:
		return i18n.Text("dockergc.reasons.older_version"), true
	case r.opts.PruneUnclaimed:
		return i18n.Text("dockergc.reasons.unclaimed"), true
	}
	return i18n.Text("dockergc.reasons.nothing_to_compare"), false
}

func (p *planner) imagesInUse(containers []types.Container) map[string][]string {
	out := map[string][]string{}
	for i := range containers {
		c := &containers[i]
		if p.removing[c.ID] {
			continue
		}
		out[c.ImageID] = append(out[c.ImageID], containerName(c))
	}
	return out
}

func (p *planner) planVolumes(ctx context.Context, containers []types.Container) error {
	if len(p.cls.projects) == 0 && !p.opts.Uninstall {
		return nil
	}
	list, err := p.api.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return apperr.Wrap(err, "dockergc.errors.list_volumes")
	}
	mounted := map[string]bool{}
	for i := range containers {
		if p.removing[containers[i].ID] {
			continue
		}
		for _, m := range containers[i].Mounts {
			if m.Name != "" {
				mounted[m.Name] = true
			}
		}
	}
	for _, v := range list.Volumes {
		if v == nil || mounted[v.Name] {
			continue
		}
		if reason, ok := p.volumeReason(v); ok {
			p.plan.Volumes = append(p.plan.Volumes, VolumeItem{Name: v.Name, Reason: reason})
		}
	}
	sort.Slice(p.plan.Volumes, func(i, j int) bool { return p.plan.Volumes[i].Name < p.plan.Volumes[j].Name })
	return nil
}

func (p *planner) volumeReason(v *volume.Volume) (string, bool) {
	if ws, ok := v.Labels[labelSandboxCache]; ok {
		if !p.opts.Uninstall {
			return "", false
		}
		return i18n.Text("dockergc.reasons.sandbox_cache", "workspace", ws), true
	}
	if scope, ok := v.Labels[labelEngineCache]; ok {
		if !p.opts.Uninstall || dockerx.ForeignInstall(v.Labels) {
			return "", false
		}
		return i18n.Text("dockergc.reasons.engine_cache", "scope", scope), true
	}
	if eng, ok := v.Labels[labelPersonalLogin]; ok {
		if !p.opts.Uninstall || dockerx.ForeignInstall(v.Labels) {
			return "", false
		}
		return i18n.Text("dockergc.reasons.personal_login", "engine", eng), true
	}
	if !p.cls.projects[v.Labels[labelComposeProject]] {
		return "", false
	}
	role := v.Labels[labelComposeVolume]
	switch {
	case cacheVolumes[role]:
		return i18n.Text("dockergc.reasons.build_cache"), true
	case p.opts.Uninstall && p.opts.PurgeData:
		if dataVolumes[role] {
			return i18n.Text("dockergc.reasons.data_role", "role", role), true
		}
		return i18n.Text("dockergc.reasons.data"), true
	}
	return "", false
}

// Attachment is read from containers, stopped ones included: a network's endpoint list only names
// running ones,
// and removing a stopped stack's network makes its next `start` fail.
func (p *planner) planNetworks(ctx context.Context, containers []types.Container) error {
	list, err := p.api.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return apperr.Wrap(err, "dockergc.errors.list_networks")
	}
	attached := map[string]bool{}
	for i := range containers {
		c := &containers[i]
		if p.removing[c.ID] || c.NetworkSettings == nil {
			continue
		}
		for name, ep := range c.NetworkSettings.Networks {
			attached[name] = true
			if ep != nil && ep.NetworkID != "" {
				attached[ep.NetworkID] = true
			}
		}
	}
	for i := range list {
		n := &list[i]
		if !p.cls.network(n.Name, n.Labels, p.opts.AgentNetwork) || attached[n.Name] || attached[n.ID] {
			continue
		}
		if p.attachedRunning(n) {
			continue
		}
		p.plan.Networks = append(p.plan.Networks, NetworkItem{ID: n.ID, Name: n.Name})
	}
	sort.Slice(p.plan.Networks, func(i, j int) bool { return p.plan.Networks[i].Name < p.plan.Networks[j].Name })
	return nil
}

// Only some engines fill network endpoints in NetworkList.
func (p *planner) attachedRunning(n *network.Summary) bool {
	for id := range n.Containers {
		if !p.removing[id] {
			return true
		}
	}
	return false
}

type Result struct {
	Containers, Images, Volumes, Networks int
	ImageBytes                            int64
	Errors                                []error
}

func (r *Result) Err() error { return errors.Join(r.Errors...) }

// Containers go first so their images and networks are free. Images are never forced, so one a
// container
// started using since the plan was made refuses instead of going.
func Apply(ctx context.Context, api API, plan *Plan) Result {
	var res Result
	fail := func(err, wrapped error) bool {
		if err == nil || errdefs.IsNotFound(err) {
			return false
		}
		res.Errors = append(res.Errors, wrapped)
		return true
	}
	for _, c := range plan.Containers {
		err := api.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: c.running, RemoveVolumes: true})
		if !fail(err, apperr.Wrap(err, "dockergc.errors.remove_container", "name", c.Name)) {
			res.Containers++
		}
	}
	for i := range plan.Images {
		if applyImage(ctx, api, &plan.Images[i], fail) {
			res.Images++
			if !plan.Images[i].Shared {
				res.ImageBytes += plan.Images[i].Size
			}
		}
	}
	for _, v := range plan.Volumes {
		err := api.VolumeRemove(ctx, v.Name, false)
		if !fail(err, apperr.Wrap(err, "dockergc.errors.remove_volume", "name", v.Name)) {
			res.Volumes++
		}
	}
	for _, n := range plan.Networks {
		err := api.NetworkRemove(ctx, n.ID)
		if !fail(err, apperr.Wrap(err, "dockergc.errors.remove_network", "name", n.Name)) {
			res.Networks++
		}
	}
	return res
}

func Sweep(ctx context.Context, api API, opts Options) (*Plan, Result, error) {
	plan, err := Scan(ctx, api, opts)
	if err != nil {
		return nil, Result{}, err
	}
	return plan, Apply(ctx, api, plan), nil
}

func applyImage(ctx context.Context, api API, img *ImageItem, fail func(err, wrapped error) bool) bool {
	opts := image.RemoveOptions{PruneChildren: true}
	if len(img.Refs) == 0 {
		_, err := api.ImageRemove(ctx, img.ID, opts)
		return !fail(err, apperr.Wrap(err, "dockergc.errors.remove_image", "image", shortID(img.ID)))
	}
	ok := true
	for _, r := range img.Refs {
		if _, err := api.ImageRemove(ctx, r, opts); fail(err, apperr.Wrap(err, "dockergc.errors.remove_image", "image", r)) {
			ok = false
		}
	}
	return ok
}

func matches(img *image.Summary, set map[string]bool) bool {
	if img == nil || len(set) == 0 {
		return false
	}
	if set[img.ID] || set[shortID(img.ID)] {
		return true
	}
	for _, r := range tagRefs(img) {
		if set[normalizeRef(r)] {
			return true
		}
	}
	for _, d := range img.RepoDigests {
		if set[normalizeRef(d)] {
			return true
		}
	}
	return false
}

func refSet(refs []string) map[string]bool {
	out := map[string]bool{}
	for _, r := range refs {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		out[normalizeRef(r)] = true
		if strings.HasPrefix(r, "sha256:") {
			out[shortID(r)] = true
		}
	}
	return out
}

func sortImages(items []ImageItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Name() < items[j].Name()
	})
}

func containerName(c *types.Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return shortID(c.ID)
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func SelfComposeProject(ctx context.Context, api API) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return ""
	}
	info, err := api.ContainerInspect(ctx, host)
	if err != nil || info.Config == nil {
		return ""
	}
	return info.Config.Labels[labelComposeProject]
}

func DiscoverProjects(ctx context.Context, api API, fallback string, workingDirs ...string) ([]string, error) {
	containers, err := api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, apperr.Wrap(err, "dockergc.errors.list_containers")
	}
	dirs := map[string]bool{}
	for _, d := range workingDirs {
		dirs[strings.TrimRight(d, "/")] = true
	}
	seen := map[string]bool{}
	var out []string
	for i := range containers {
		l := containers[i].Labels
		project := l[labelComposeProject]
		if project == "" || seen[project] || !dirs[strings.TrimRight(l[labelComposeWorkingDir], "/")] {
			continue
		}
		seen[project] = true
		out = append(out, project)
	}
	if fallback == "" || seen[fallback] {
		sort.Strings(out)
		return out, nil
	}
	vols, err := api.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, apperr.Wrap(err, "dockergc.errors.list_volumes")
	}
	for _, v := range vols.Volumes {
		if v != nil && v.Labels[labelComposeProject] == fallback && dataVolumes[v.Labels[labelComposeVolume]] {
			out = append(out, fallback)
			break
		}
	}
	sort.Strings(out)
	return out, nil
}
