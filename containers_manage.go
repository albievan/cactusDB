package main

// ☰ → DB containers → a container's page: its settings, what it uses (CPU,
// memory, storage), and start, stop, restart, delete and purge. Delete keeps
// the data; purge removes everything the container had: the container, its
// data folder or volume and, when nothing else uses it, its image. Looking
// is open to everyone who sees the list; the actions need the superuser.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type kv struct {
	K string `json:"k"`
	V string `json:"v"`
}

// ctrUsage is what a container uses. Sizes are bytes; -1 is unknown.
type ctrUsage struct {
	Measured bool     `json:"measured"` // CPU and memory are known
	CPUPct   float64  `json:"cpuPct"`   // percent of one core (docker stats' way: 2 busy cores are 200%)
	CPUs     float64  `json:"cpus"`     // the cores the container can use, 0 when unknown
	MemUsed  int64    `json:"memUsed"`
	MemLimit int64    `json:"memLimit"` // its limit, or all the memory Docker has; 0 when unknown
	Data     int64    `json:"data"`     // the database files
	Layer    int64    `json:"layer"`    // what the container wrote outside its data folder (docker)
	Image    int64    `json:"image"`
	Capacity int64    `json:"capacity"` // the volume claim's size (kubernetes), 0 without one
	NetIO    string   `json:"netIO,omitempty"`
	BlockIO  string   `json:"blockIO,omitempty"`
	PIDs     string   `json:"pids,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

type ctrDetail struct {
	deployedCtr
	SpecName string   `json:"specName"`
	Running  bool     `json:"running"`
	Settings []kv     `json:"settings"`
	Env      []kv     `json:"env"`
	Hidden   []string `json:"hiddenEnv"` // the variables whose value is hidden: the superuser can reveal them
	Usage    ctrUsage `json:"usage"`
	Purge    []string `json:"purge"` // what purge removes
	Checked  string   `json:"checked"`
}

var secretEnvRe = regexp.MustCompile(`(?i)pass|secret|key|token|auth|credential`)

// maskEnv hides values that look like passwords; the rest is shown as set.
func maskEnv(name, value string) string {
	if value != "" && secretEnvRe.MatchString(name) {
		return "•••••• (hidden)"
	}
	return value
}

// ---- sizes -------------------------------------------------------------------------

// parseSize reads docker's sizes ("39.7MiB", "1.2GB", "512B") and
// Kubernetes quantities ("120Mi", "10Gi", "500M").
func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	num, unit := s, ""
	if i >= 0 {
		num, unit = s[:i], strings.TrimSpace(s[i:])
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return -1
	}
	mult := map[string]float64{"": 1, "B": 1,
		"k": 1e3, "kB": 1e3, "KB": 1e3, "M": 1e6, "MB": 1e6, "G": 1e9, "GB": 1e9, "T": 1e12, "TB": 1e12,
		"Ki": 1 << 10, "KiB": 1 << 10, "Mi": 1 << 20, "MiB": 1 << 20, "Gi": 1 << 30, "GiB": 1 << 30, "Ti": 1 << 40, "TiB": 1 << 40}[unit]
	if mult == 0 {
		return -1
	}
	return int64(f * mult)
}

// parseCPU reads a Kubernetes CPU quantity ("250m", "2") as percent of one core.
func parseCPU(s string) float64 {
	if m, ok := strings.CutSuffix(s, "m"); ok {
		f, _ := strconv.ParseFloat(m, 64)
		return f / 10
	}
	if n, ok := strings.CutSuffix(s, "n"); ok { // nanocores
		f, _ := strconv.ParseFloat(n, 64)
		return f / 1e7
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f * 100
}

// The data folder's size is measured at most once a minute: walking a large
// database on every refresh of the page would be work for nothing.
var ctrSizes = struct {
	sync.Mutex
	m map[string]sizeAt
}{m: map[string]sizeAt{}}

type sizeAt struct {
	n  int64
	at time.Time
}

func sizeKey(c deployedCtr) string { return c.Runtime + "/" + c.Namespace + "/" + c.Name }

func cachedSize(key string, measure func() int64) int64 {
	ctrSizes.Lock()
	c, ok := ctrSizes.m[key]
	ctrSizes.Unlock()
	if ok && time.Since(c.at) < time.Minute {
		return c.n
	}
	n := measure()
	if n >= 0 {
		ctrSizes.Lock()
		ctrSizes.m[key] = sizeAt{n, time.Now()}
		ctrSizes.Unlock()
	}
	return n
}

// dirSize adds up the files in a folder; -1 when some can't be read.
func dirSize(ctx context.Context, dir string) int64 {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e.Type().IsRegular() {
			if fi, err := e.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	if err != nil {
		return -1
	}
	return n
}

// duOutput reads `du -sk` (KB, then the path) as bytes.
func duOutput(out string) int64 {
	l := strings.Split(strings.TrimSpace(out), "\n")
	f := strings.Fields(l[len(l)-1])
	if len(f) == 0 {
		return -1
	}
	k, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return -1
	}
	return k * 1024
}

func fmtWhen(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 2000 {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05") + " (" + ago(time.Since(t)) + " ago)"
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + " s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + " days"
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// ---- docker --------------------------------------------------------------------------

type dockerInspect struct {
	ID           string `json:"Id"`
	Created      string
	Image        string // its ID
	RestartCount int
	SizeRw       *int64
	State        struct {
		Status     string
		Running    bool
		StartedAt  string
		FinishedAt string
		ExitCode   int
		OOMKilled  bool
		Health     *struct{ Status string }
	}
	Config struct {
		Image  string
		Env    []string
		Cmd    []string
		Labels map[string]string
	}
	HostConfig struct {
		RestartPolicy struct{ Name string }
		PortBindings  map[string][]struct{ HostIp, HostPort string }
		Memory        int64
		NanoCpus      int64
	}
	Mounts []struct {
		Type, Name, Source, Destination string
	}
}

func dockerInspectOne(ctx context.Context, rt *ctrRuntime, name string, size bool) (*dockerInspect, error) {
	args := []string{"inspect", "--type", "container"}
	if size {
		args = append(args, "--size")
	}
	out, err := runTool(ctx, toolCmd(ctx, rt.tool, append(args, name)...), nil)
	if err != nil {
		return nil, err
	}
	var v []dockerInspect
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v) == 0 {
		return nil, fmt.Errorf("docker inspect %s: unexpected answer", name)
	}
	return &v[0], nil
}

// dataMount is where the database's files are: a folder (bind) or a volume.
func (in *dockerInspect) dataMount(sp containerSpec) (bind, volume string) {
	for _, m := range in.Mounts {
		if m.Destination == sp.DataPath || sp.DataPath == "" {
			if m.Type == "bind" {
				return m.Source, ""
			}
			if m.Type == "volume" {
				return "", m.Name
			}
		}
	}
	return "", ""
}

func dockerDetail(ctx context.Context, rt *ctrRuntime, c deployedCtr) (*ctrDetail, error) {
	in, err := dockerInspectOne(ctx, rt, c.Name, true)
	if err != nil {
		return nil, err
	}
	sp, _ := specByID(c.Spec)
	d := &ctrDetail{deployedCtr: c, SpecName: sp.Name, Running: in.State.Running, Settings: []kv{}, Env: []kv{},
		Usage: ctrUsage{Data: -1, Layer: -1, Image: -1}}
	if in.SizeRw != nil {
		d.Usage.Layer = *in.SizeRw
	}
	add := func(k, v string) {
		if v != "" {
			d.Settings = append(d.Settings, kv{k, v})
		}
	}
	add("Database", sp.Name)
	add("Runs on", strings.TrimSpace(rt.Name+" "+rt.Version))
	add("Container ID", shortID(in.ID))
	add("Image", in.Config.Image)
	add("Image ID", shortID(in.Image))
	state := in.State.Status
	if in.State.Health != nil {
		state += ", health: " + in.State.Health.Status
	}
	if !in.State.Running && in.State.Status == "exited" {
		state += fmt.Sprintf(", exit code %d", in.State.ExitCode)
	}
	if in.State.OOMKilled {
		state += ", stopped for lack of memory"
	}
	add("State", state)
	add("Created", fmtWhen(in.Created))
	if in.State.Running {
		add("Started", fmtWhen(in.State.StartedAt))
	} else {
		add("Stopped", fmtWhen(in.State.FinishedAt))
	}
	restart := in.HostConfig.RestartPolicy.Name
	if in.RestartCount > 0 {
		restart += fmt.Sprintf(" (restarted %d times)", in.RestartCount)
	}
	add("Restart policy", restart)
	var ports []string
	for cport, binds := range in.HostConfig.PortBindings {
		for _, b := range binds {
			host := b.HostIp
			if host == "" || host == "0.0.0.0" {
				host = "every interface"
			}
			ports = append(ports, fmt.Sprintf("%s port %s → %s", host, b.HostPort, cport))
		}
	}
	sort.Strings(ports)
	add("Ports", strings.Join(ports, "; "))
	bind, vol := in.dataMount(sp)
	for _, m := range in.Mounts {
		switch m.Type {
		case "bind":
			add("Data folder", m.Source+" → "+m.Destination)
		case "volume":
			what := "Volume"
			if m.Name == vol {
				what = "Data volume"
			}
			add(what, m.Name+" → "+m.Destination)
		}
	}
	if len(in.Config.Cmd) > 0 {
		add("Command", strings.Join(in.Config.Cmd, " "))
	}
	if in.HostConfig.Memory > 0 {
		add("Memory limit", strconv.FormatInt(in.HostConfig.Memory>>20, 10)+" MB")
	}
	if in.HostConfig.NanoCpus > 0 {
		add("CPU limit", strconv.FormatFloat(float64(in.HostConfig.NanoCpus)/1e9, 'f', -1, 64)+" cores")
	}
	var labels []string
	for k, v := range in.Config.Labels {
		if strings.HasPrefix(k, "com.cactusdb.") {
			labels = append(labels, k+"="+v)
		}
	}
	sort.Strings(labels)
	add("Labels", strings.Join(labels, ", "))
	for _, e := range in.Config.Env {
		k, v, _ := strings.Cut(e, "=")
		d.Env = append(d.Env, kv{k, maskEnv(k, v)})
		if maskEnv(k, v) != v {
			d.Hidden = append(d.Hidden, k)
		}
	}

	d.Purge = []string{"the container " + c.Name}
	switch {
	case bind != "":
		d.Purge = append(d.Purge, "the data folder "+bind+" and every file in it")
	case vol != "":
		d.Purge = append(d.Purge, "the Docker volume "+vol)
	}
	d.Purge = append(d.Purge, "the image "+in.Config.Image+", unless another container uses it")

	// The rest is measured side by side: docker stats alone takes a second or two.
	var wg sync.WaitGroup
	var mu sync.Mutex
	note := func(s string) { mu.Lock(); d.Usage.Notes = append(d.Usage.Notes, s); mu.Unlock() }
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	if in.State.Running {
		run(func() {
			out, err := runTool(ctx, toolCmd(ctx, rt.tool, "stats", "--no-stream", "--format", "{{json .}}", c.Name), nil)
			var st struct{ CPUPerc, MemUsage, NetIO, BlockIO, PIDs string }
			if err != nil || json.Unmarshal([]byte(strings.TrimSpace(out)), &st) != nil {
				note("CPU and memory aren't known: docker stats didn't answer")
				return
			}
			used, limit, _ := strings.Cut(st.MemUsage, "/")
			mu.Lock()
			d.Usage.Measured = true
			d.Usage.CPUPct, _ = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(st.CPUPerc), "%"), 64)
			d.Usage.MemUsed, d.Usage.MemLimit = parseSize(used), parseSize(limit)
			d.Usage.NetIO, d.Usage.BlockIO, d.Usage.PIDs = st.NetIO, st.BlockIO, st.PIDs
			mu.Unlock()
		})
	}
	run(func() {
		if out, err := runTool(ctx, toolCmd(ctx, rt.tool, "info", "--format", "{{.NCPU}}"), nil); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(out))
			mu.Lock()
			d.Usage.CPUs = float64(n)
			if in.HostConfig.NanoCpus > 0 {
				d.Usage.CPUs = float64(in.HostConfig.NanoCpus) / 1e9
			}
			mu.Unlock()
		}
	})
	run(func() {
		if out, err := runTool(ctx, toolCmd(ctx, rt.tool, "image", "inspect", "--format", "{{.Size}}", in.Image), nil); err == nil {
			n, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
			mu.Lock()
			d.Usage.Image = n
			mu.Unlock()
		}
	})
	run(func() {
		n := cachedSize(sizeKey(c), func() int64 {
			// From inside: the files belong to the database's user, which
			// cactusdb may not be able to read.
			if in.State.Running && sp.DataPath != "" {
				out, _ := runTool(ctx, toolCmd(ctx, rt.tool, "exec", c.Name, "du", "-sk", sp.DataPath), nil)
				if n := duOutput(out); n >= 0 {
					return n
				}
			}
			if bind != "" {
				return dirSize(ctx, bind)
			}
			return -1
		})
		mu.Lock()
		d.Usage.Data = n
		mu.Unlock()
		if n < 0 && !in.State.Running {
			note("the size of the data is measured while the container runs")
		}
	})
	wg.Wait()
	return d, nil
}

// ---- kubernetes ------------------------------------------------------------------------

type kubeDeployment struct {
	Metadata struct {
		CreationTimestamp string
		Annotations       map[string]string
	}
	Spec struct {
		Replicas int
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string
					Image string
					Args  []string
					Env   []struct {
						Name      string
						Value     string
						ValueFrom *struct {
							SecretKeyRef *struct{ Name, Key string }
						}
					}
					Resources struct {
						Requests map[string]string
						Limits   map[string]string
					}
					VolumeMounts []struct{ Name, MountPath string }
				}
				Volumes []struct {
					Name                  string
					HostPath              *struct{ Path string }
					PersistentVolumeClaim *struct{ ClaimName string }
				}
			}
		}
	}
	Status struct{ ReadyReplicas int }
}

type kubePod struct {
	Metadata struct{ Name string }
	Spec     struct{ NodeName string }
	Status   struct {
		Phase, PodIP, StartTime string
		ContainerStatuses       []struct {
			Name         string
			Ready        bool
			RestartCount int
			State        struct {
				Waiting *struct{ Reason string }
			}
		}
	}
}

func kubeJSON(ctx context.Context, rt *ctrRuntime, v any, args ...string) error {
	out, err := runTool(ctx, toolCmd(ctx, rt.tool, append(args, "-o", "json", "--request-timeout=10s")...), nil)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), v)
}

// kubeData is the deployment's data volume: a folder on the node (hostPath) or a claim.
func (dep *kubeDeployment) data() (hostPath, claim string) {
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Name != "data" {
			continue
		}
		if v.HostPath != nil {
			return v.HostPath.Path, ""
		}
		if v.PersistentVolumeClaim != nil {
			return "", v.PersistentVolumeClaim.ClaimName
		}
	}
	return "", ""
}

func kubePods(ctx context.Context, rt *ctrRuntime, c deployedCtr) []kubePod {
	var pods struct{ Items []kubePod }
	_ = kubeJSON(ctx, rt, &pods, "get", "pods", "-n", c.Namespace, "-l", "app.kubernetes.io/instance="+c.Name)
	return pods.Items
}

func kubeDetail(ctx context.Context, rt *ctrRuntime, c deployedCtr) (*ctrDetail, error) {
	var dep kubeDeployment
	if err := kubeJSON(ctx, rt, &dep, "get", "deployment", c.Name, "-n", c.Namespace); err != nil {
		return nil, err
	}
	sp, _ := specByID(c.Spec)
	d := &ctrDetail{deployedCtr: c, SpecName: sp.Name, Settings: []kv{}, Env: []kv{},
		Usage: ctrUsage{Data: -1, Layer: -1, Image: -1}}
	add := func(k, v string) {
		if v != "" {
			d.Settings = append(d.Settings, kv{k, v})
		}
	}
	add("Database", sp.Name)
	add("Runs on", strings.TrimSpace(rt.Name+" "+rt.Version))
	add("Context", rt.Context)
	add("Namespace", c.Namespace)
	add("Created", fmtWhen(dep.Metadata.CreationTimestamp))
	add("Replicas", fmt.Sprintf("%d wanted, %d ready", dep.Spec.Replicas, dep.Status.ReadyReplicas))
	var ctr0 = dep.Spec.Template.Spec.Containers
	if len(ctr0) > 0 {
		k := ctr0[0]
		add("Image", k.Image)
		if len(k.Args) > 0 {
			add("Arguments", strings.Join(k.Args, " "))
		}
		if m := k.Resources.Requests["memory"]; m != "" {
			add("Memory requested", m)
		}
		if m := k.Resources.Limits["memory"]; m != "" {
			add("Memory limit", m)
			d.Usage.MemLimit = parseSize(m)
		}
		if m := k.Resources.Limits["cpu"]; m != "" {
			add("CPU limit", m)
			d.Usage.CPUs = parseCPU(m) / 100
		}
		for _, e := range k.Env {
			v := maskEnv(e.Name, e.Value)
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				v = "from the secret " + e.ValueFrom.SecretKeyRef.Name
			}
			if v != e.Value {
				d.Hidden = append(d.Hidden, e.Name)
			}
			d.Env = append(d.Env, kv{e.Name, v})
		}
	}
	host, claim := dep.data()
	if host != "" {
		add("Data folder", host+" on the node (hostPath) → "+sp.DataPath)
	}
	if claim != "" {
		var pvc struct {
			Spec struct {
				StorageClassName *string
				VolumeName       string
			}
			Status struct {
				Phase    string
				Capacity map[string]string
			}
		}
		if err := kubeJSON(ctx, rt, &pvc, "get", "pvc", claim, "-n", c.Namespace); err == nil {
			cls := "default"
			if pvc.Spec.StorageClassName != nil {
				cls = *pvc.Spec.StorageClassName
			}
			add("Data volume", fmt.Sprintf("claim %s (%s, %s, storage class %s) → %s", claim, pvc.Status.Capacity["storage"], strings.ToLower(pvc.Status.Phase), cls, sp.DataPath))
			if s := pvc.Status.Capacity["storage"]; s != "" {
				d.Usage.Capacity = parseSize(s)
			}
		} else {
			add("Data volume", "claim "+claim)
		}
	}
	var svc struct {
		Spec struct {
			Type      string
			ClusterIP string
			Ports     []struct {
				Port       int
				NodePort   int
				TargetPort any
			}
		}
		Status struct {
			LoadBalancer struct {
				Ingress []struct{ IP, Hostname string }
			}
		}
	}
	if err := kubeJSON(ctx, rt, &svc, "get", "service", c.Name, "-n", c.Namespace); err == nil {
		var ps []string
		for _, p := range svc.Spec.Ports {
			s := fmt.Sprintf("port %d → %v", p.Port, p.TargetPort)
			if p.NodePort > 0 {
				s += fmt.Sprintf(", node port %d", p.NodePort)
			}
			ps = append(ps, s)
		}
		add("Service", svc.Spec.Type+" · cluster IP "+svc.Spec.ClusterIP+" · "+strings.Join(ps, "; "))
		var ing []string
		for _, i := range svc.Status.LoadBalancer.Ingress {
			ing = append(ing, i.IP+i.Hostname)
		}
		add("Load balancer", strings.Join(ing, ", "))
	}
	pods := kubePods(ctx, rt, c)
	for _, p := range pods {
		st := strings.ToLower(p.Status.Phase)
		restarts := 0
		for _, cs := range p.Status.ContainerStatuses {
			restarts += cs.RestartCount
			if cs.State.Waiting != nil {
				st += ", " + cs.State.Waiting.Reason
			}
		}
		if p.Status.Phase == "Running" {
			d.Running = true
		}
		add("Pod "+p.Metadata.Name, fmt.Sprintf("%s · node %s · IP %s · restarted %d times · started %s", st, p.Spec.NodeName, p.Status.PodIP, restarts, fmtWhen(p.Status.StartTime)))
	}
	if len(pods) == 0 {
		add("Pods", "none (stopped)")
	}

	d.Purge = []string{"the deployment, service and secret " + c.Name}
	switch {
	case host != "":
		d.Purge = append(d.Purge, "the folder "+host+" on the node, and every file in it")
	case claim != "":
		d.Purge = append(d.Purge, "the volume claim "+claim+" and its data")
	}
	if len(ctr0) > 0 {
		d.Purge = append(d.Purge, "not the image "+ctr0[0].Image+": the nodes remove unused images themselves")
	}

	if d.Running {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			out, err := runTool(ctx, toolCmd(ctx, rt.tool, "top", "pod", "-n", c.Namespace, "-l", "app.kubernetes.io/instance="+c.Name, "--no-headers", "--request-timeout=10s"), nil)
			if err != nil {
				d.Usage.Notes = append(d.Usage.Notes, "CPU and memory aren't known: kubectl top needs the cluster's metrics-server ("+firstLine(err.Error())+")")
				return
			}
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if f := strings.Fields(l); len(f) >= 3 {
					d.Usage.Measured = true
					d.Usage.CPUPct += parseCPU(f[1])
					d.Usage.MemUsed += parseSize(f[2])
				}
			}
		}()
		var n int64 = -1
		go func() {
			defer wg.Done()
			n = cachedSize(sizeKey(c), func() int64 {
				out, _ := runTool(ctx, toolCmd(ctx, rt.tool, "exec", "-n", c.Namespace, "deployment/"+c.Name, "-c", "db", "--", "du", "-sk", sp.DataPath), nil)
				return duOutput(out)
			})
		}()
		wg.Wait()
		d.Usage.Data = n
	} else {
		d.Usage.Notes = append(d.Usage.Notes, "CPU, memory and the size of the data are measured while it runs")
	}
	return d, nil
}

// ---- actions ---------------------------------------------------------------------------

// purgeableDir refuses folders whose deletion would take more than a
// database with it: a disk's root or a folder one level down, the home
// folder, or a folder that holds cactusdb itself.
func purgeableDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%s isn't a full path", dir)
	}
	clean := filepath.Clean(dir)
	n := 0
	for _, p := range strings.Split(filepath.ToSlash(strings.TrimPrefix(clean, filepath.VolumeName(clean))), "/") {
		if p != "" {
			n++
		}
	}
	if n < 2 {
		return fmt.Errorf("%s is too close to the root of the disk to delete", clean)
	}
	var keep []string
	if h, err := os.UserHomeDir(); err == nil {
		keep = append(keep, h)
	}
	if wd, err := os.Getwd(); err == nil {
		keep = append(keep, wd)
	}
	if exe, err := os.Executable(); err == nil {
		keep = append(keep, filepath.Dir(exe))
	}
	sep := string(filepath.Separator)
	// Windows and macOS disks don't tell C:\Users\Bob from c:\users\bob.
	fold := func(s string) string {
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			return strings.ToLower(s)
		}
		return s
	}
	for _, sys := range systemDirs {
		if fold(clean) == fold(filepath.Clean(filepath.FromSlash(sys))) {
			return fmt.Errorf("%s is a system folder: cactusdb won't delete it", clean)
		}
	}
	for _, k := range keep {
		k = filepath.Clean(k)
		if fold(clean) == fold(k) || strings.HasPrefix(fold(k+sep), fold(clean+sep)) {
			return fmt.Errorf("%s holds %s: cactusdb won't delete it", clean, k)
		}
	}
	return nil
}

// systemDirs are folders two levels down that hold far more than a
// database (a data folder that was one of them is not purged).
var systemDirs = []string{"/var/lib", "/var/log", "/var/run", "/var/cache", "/var/opt", "/usr/local", "/usr/lib", "/usr/bin",
	"/usr/share", "/usr/sbin", "/opt/homebrew", "/etc/init.d", "/Library/Application Support", "/System/Library",
	"/private/var", "/private/etc"}

// removeDataDir deletes the data folder. The database's files belong to its
// user, so what cactusdb can't delete itself is deleted from a container of
// the database's image running as root.
func removeDataDir(ctx context.Context, rt *ctrRuntime, dir, image string) error {
	if err := os.RemoveAll(dir); err == nil {
		return nil
	}
	if _, err := runTool(ctx, toolCmd(ctx, rt.tool, "run", "--rm", "--user", "0", "--entrypoint", "sh", "-v", dir+":/purge", image,
		"-c", "rm -rf /purge/..?* /purge/.[!.]* /purge/*"), nil); err != nil {
		return fmt.Errorf("couldn't delete the files in %s: %w", dir, err)
	}
	return os.RemoveAll(dir)
}

func purgeDocker(ctx context.Context, rt *ctrRuntime, c deployedCtr) ([]string, error) {
	in, err := dockerInspectOne(ctx, rt, c.Name, false)
	if err != nil {
		return nil, err
	}
	sp, _ := specByID(c.Spec)
	bind, vol := in.dataMount(sp)
	if bind != "" {
		if err := purgeableDir(bind); err != nil {
			return nil, fmt.Errorf("nothing was purged: %w", err)
		}
	}
	var done []string
	var errs []error
	// -v also removes the anonymous volumes some images declare.
	if _, err := runTool(ctx, toolCmd(ctx, rt.tool, "rm", "-f", "-v", c.Name), nil); err != nil {
		return nil, err
	}
	done = append(done, "removed the container "+c.Name)
	if bind != "" {
		if err := removeDataDir(ctx, rt, bind, in.Image); err != nil {
			errs = append(errs, err)
		} else {
			done = append(done, "deleted the data folder "+bind)
		}
	}
	if vol != "" {
		if _, err := runTool(ctx, toolCmd(ctx, rt.tool, "volume", "rm", vol), nil); err != nil {
			errs = append(errs, fmt.Errorf("removing the volume %s: %w", vol, err))
		} else {
			done = append(done, "removed the volume "+vol)
		}
	}
	users, err := runTool(ctx, toolCmd(ctx, rt.tool, "ps", "-a", "-q", "--filter", "ancestor="+in.Image), nil)
	switch n := len(strings.Fields(users)); {
	case err != nil:
		errs = append(errs, fmt.Errorf("checking who uses the image: %w", err))
	case n > 0:
		done = append(done, fmt.Sprintf("kept the image %s: %d other container(s) use it", in.Config.Image, n))
	default:
		if _, err := runTool(ctx, toolCmd(ctx, rt.tool, "rmi", in.Config.Image), nil); err != nil {
			errs = append(errs, fmt.Errorf("removing the image %s: %w", in.Config.Image, err))
		} else {
			done = append(done, "removed the image "+in.Config.Image)
		}
	}
	return done, errors.Join(errs...)
}

// kubePurgePod deletes a hostPath data folder on its node: a pod on that node
// mounts the folder's parent and removes the folder.
func kubePurgePod(name, ns, node, dir string) []byte {
	parent, base := path.Dir(filepath.ToSlash(dir)), path.Base(filepath.ToSlash(dir))
	pod := obj{"apiVersion": "v1", "kind": "Pod",
		"metadata": obj{"name": name + "-purge", "namespace": ns, "labels": obj{"app.kubernetes.io/managed-by": "cactusdb", "app.kubernetes.io/instance": name}},
		"spec": obj{"nodeName": node, "restartPolicy": "Never",
			"containers": []any{obj{"name": "purge", "image": "busybox:1.36", "command": []any{"rm", "-rf", "--", "/parent/" + base},
				"securityContext": obj{"runAsUser": 0}, "volumeMounts": []any{obj{"name": "parent", "mountPath": "/parent"}}}},
			"volumes": []any{obj{"name": "parent", "hostPath": obj{"path": parent, "type": "Directory"}}}}}
	b, _ := json.Marshal(pod)
	return b
}

func purgeKubernetes(ctx context.Context, rt *ctrRuntime, c deployedCtr) ([]string, error) {
	var dep kubeDeployment
	if err := kubeJSON(ctx, rt, &dep, "get", "deployment", c.Name, "-n", c.Namespace); err != nil {
		return nil, err
	}
	host, claim := dep.data()
	node := ""
	if host != "" {
		if err := purgeableDir(host); err != nil {
			return nil, fmt.Errorf("nothing was purged: %w", err)
		}
		for _, p := range kubePods(ctx, rt, c) {
			if p.Spec.NodeName != "" {
				node = p.Spec.NodeName
			}
		}
		if node == "" {
			return nil, fmt.Errorf("nothing was purged: the data folder %s is on the node the pod runs on, and no pod runs now: start it first, so cactusdb knows which node", host)
		}
	}
	var done []string
	var errs []error
	sel := "app.kubernetes.io/managed-by=cactusdb,app.kubernetes.io/instance=" + c.Name
	if _, err := runTool(ctx, toolCmd(ctx, rt.tool, "delete", "deployment,service,secret", "-n", c.Namespace, "-l", sel, "--wait=true", "--timeout=3m"), nil); err != nil {
		return nil, err
	}
	done = append(done, "removed the deployment, service and secret "+c.Name)
	if claim != "" {
		if _, err := runTool(ctx, toolCmd(ctx, rt.tool, "delete", "pvc", claim, "-n", c.Namespace, "--wait=false"), nil); err != nil {
			errs = append(errs, fmt.Errorf("removing the volume claim %s: %w", claim, err))
		} else {
			done = append(done, "removed the volume claim "+claim+" (its volume goes with it when the storage class deletes on release)")
		}
	}
	if host != "" {
		pod := c.Name + "-purge"
		_, err := runTool(ctx, toolCmd(ctx, rt.tool, "apply", "-f", "-"), kubePurgePod(c.Name, c.Namespace, node, host))
		if err == nil {
			_, err = runTool(ctx, toolCmd(ctx, rt.tool, "wait", "pod/"+pod, "-n", c.Namespace, "--for=jsonpath={.status.phase}=Succeeded", "--timeout=3m"), nil)
		}
		_, _ = runTool(ctx, toolCmd(ctx, rt.tool, "delete", "pod", pod, "-n", c.Namespace, "--wait=false", "--ignore-not-found"), nil)
		if err != nil {
			errs = append(errs, fmt.Errorf("deleting %s on node %s: %w", host, node, err))
		} else {
			done = append(done, "deleted the data folder "+host+" on node "+node)
		}
	}
	if k := dep.Spec.Template.Spec.Containers; len(k) > 0 {
		done = append(done, "the image "+k[0].Image+" stays in the nodes' cache until they remove unused images")
	}
	return done, errors.Join(errs...)
}

// deleteCtr removes the container (docker) or the deployment, service and
// secret (kubernetes); the data stays.
func deleteCtr(ctx context.Context, rt *ctrRuntime, t ctrTarget) error {
	if t.Runtime == "docker" {
		_, err := runTool(ctx, toolCmd(ctx, rt.tool, "rm", "-f", t.Name), nil)
		return err
	}
	_, err := runTool(ctx, toolCmd(ctx, rt.tool, "delete", "deployment,service,secret", "-n", t.Namespace,
		"-l", "app.kubernetes.io/managed-by=cactusdb,app.kubernetes.io/instance="+t.Name, "--wait=false"), nil)
	return err
}

// ---- web ------------------------------------------------------------------------------

func (s *server) handleContainerInspect(w http.ResponseWriter, r *http.Request) {
	t, rt, c, ok := s.ctrTarget(w, r, "look at a database container", false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	var d *ctrDetail
	var err error
	if t.Runtime == "docker" {
		d, err = dockerDetail(ctx, rt, c)
	} else {
		d, err = kubeDetail(ctx, rt, c)
	}
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	d.Checked = time.Now().Format(time.RFC3339)
	writeJSON(w, 200, d)
}

// handleContainerReveal shows the superuser the value of a hidden environment
// variable of the container (a password): docker's own setting, or on
// Kubernetes the value it takes from its secret.
func (s *server) handleContainerReveal(w http.ResponseWriter, r *http.Request) {
	t, rt, c, ok := s.ctrTarget(w, r, "see a hidden setting of a database container", true)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	v, err := ctrEnvValue(ctx, rt, c, t.Key)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	logAction(r, "superuser revealed %s of container %s", t.Key, t.Name)
	writeJSON(w, 200, map[string]string{"key": t.Key, "value": v})
}

// ctrEnvValue is the value of the environment variable key of container c.
func ctrEnvValue(ctx context.Context, rt *ctrRuntime, c deployedCtr, key string) (string, error) {
	if c.Runtime == "docker" {
		in, err := dockerInspectOne(ctx, rt, c.Name, false)
		if err != nil {
			return "", err
		}
		for _, e := range in.Config.Env {
			if k, v, _ := strings.Cut(e, "="); k == key {
				return v, nil
			}
		}
		return "", fmt.Errorf("%s has no setting %s", c.Name, key)
	}
	var dep kubeDeployment
	if err := kubeJSON(ctx, rt, &dep, "get", "deployment", c.Name, "-n", c.Namespace); err != nil {
		return "", err
	}
	for _, k := range dep.Spec.Template.Spec.Containers {
		for _, e := range k.Env {
			if e.Name != key {
				continue
			}
			if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
				return e.Value, nil
			}
			ref := e.ValueFrom.SecretKeyRef
			var sec struct{ Data map[string]string }
			if err := kubeJSON(ctx, rt, &sec, "get", "secret", ref.Name, "-n", c.Namespace); err != nil {
				return "", err
			}
			raw, err := base64.StdEncoding.DecodeString(sec.Data[ref.Key])
			if err != nil {
				return "", fmt.Errorf("the secret %s: %w", ref.Name, err)
			}
			return string(raw), nil
		}
	}
	return "", fmt.Errorf("%s has no setting %s", c.Name, key)
}

func (s *server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	t, rt, c, ok := s.ctrTarget(w, r, "manage a database container", true)
	if !ok {
		return
	}
	if t.Action == "purge" && t.Confirm != t.Name {
		writeErr(w, 400, errors.New("type the container's name to purge it"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	docker := t.Runtime == "docker"
	run := func(args ...string) error { _, err := runTool(ctx, toolCmd(ctx, rt.tool, args...), nil); return err }
	ns := []string{"-n", t.Namespace}
	var done []string
	var err error
	switch t.Action {
	case "start":
		if docker {
			err = run("start", t.Name)
		} else {
			err = run(append([]string{"scale", "deployment/" + t.Name, "--replicas=1"}, ns...)...)
		}
	case "stop":
		if docker {
			err = run("stop", "-t", "30", t.Name)
		} else {
			err = run(append([]string{"scale", "deployment/" + t.Name, "--replicas=0"}, ns...)...)
		}
	case "restart":
		if docker {
			err = run("restart", "-t", "30", t.Name)
		} else {
			err = run(append([]string{"rollout", "restart", "deployment/" + t.Name}, ns...)...)
		}
	case "delete":
		err = deleteCtr(ctx, rt, t)
	case "purge":
		if docker {
			done, err = purgeDocker(ctx, rt, c)
		} else {
			done, err = purgeKubernetes(ctx, rt, c)
		}
	default:
		writeErr(w, 400, fmt.Errorf("unknown action %q", t.Action))
		return
	}
	ctrSizes.Lock()
	delete(ctrSizes.m, sizeKey(c))
	ctrSizes.Unlock()
	if err != nil {
		logAction(r, "%s container %s FAILED: %s", t.Action, t.Name, firstLine(err.Error()))
		s.event(r, "container", true, "%s container %s FAILED: %s", t.Action, t.Name, firstLine(err.Error()))
		if len(done) > 0 { // a purge that got part of the way
			writeJSON(w, 502, map[string]any{"error": err.Error(), "done": done})
			return
		}
		writeErr(w, 502, err)
		return
	}
	switch t.Action {
	case "delete":
		logAction(r, "removed container %s (%s); its data was kept", t.Name, rt.Name)
		s.event(r, "container", false, "removed container %s (%s); its data was kept", t.Name, rt.Name)
	case "purge":
		logAction(r, "purged container %s (%s): %s", t.Name, rt.Name, strings.Join(done, "; "))
		s.event(r, "container", false, "purged container %s (%s): %s", t.Name, rt.Name, strings.Join(done, "; "))
	default:
		logAction(r, "%s container %s (%s)", t.Action, t.Name, rt.Name)
		s.event(r, "container", false, "%s container %s (%s)", t.Action, t.Name, rt.Name)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "done": done})
}
