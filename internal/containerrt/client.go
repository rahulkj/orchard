// Package containerrt shells out to Apple's `container` CLI. Every
// Kubernetes node this tool creates is one apple/container VM; this is the
// only package that invokes the binary directly.
package containerrt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const Bin = "container"

// Client wraps the container CLI. Zero value is usable.
type Client struct {
	capAddOnce sync.Once
	capAdd     bool // cached probe result for --cap-add support
}

// CmdError carries the captured output of a failed invocation.
type CmdError struct {
	Args   []string
	Output string
	Err    error
}

func (e *CmdError) Error() string {
	out := strings.TrimSpace(e.Output)
	return fmt.Sprintf("container %s: %v\n%s", strings.Join(e.Args, " "), e.Err, out)
}

// run executes the container CLI and returns its stdout on success. Stdout
// and stderr are captured separately so callers that parse stdout (e.g.
// Exec, for reading a file or command output from inside a node) never see
// stderr diagnostics mixed into the value they parse; on failure the error
// carries both streams for diagnostics.
func run(args ...string) (string, error) {
	cmd := exec.Command(Bin, args...)
	var stdout, combined bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, &combined)
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		return combined.String(), &CmdError{Args: args, Output: combined.String(), Err: err}
	}
	return stdout.String(), nil
}

// Available reports whether the container binary is on PATH.
func Available() bool {
	_, err := exec.LookPath(Bin)
	return err == nil
}

// SystemRunning reports whether the container system service is up.
func SystemRunning() bool {
	out, err := run("system", "status")
	if err != nil {
		return false
	}
	return !strings.Contains(strings.ToLower(out), "not running")
}

// SystemStart starts the container system service.
func SystemStart() error {
	_, err := run("system", "start")
	return err
}

// NodeSpec describes one node VM to boot.
type NodeSpec struct {
	Name   string
	Image  string
	CPUs   string
	Memory string
}

// RunDetached boots a node VM from a node image (systemd + containerd +
// kubeadm/kubelet baked in). Nodes get the full capability set: the VM
// boundary is the real isolation here, not the container's capability mask,
// and systemd inside the guest needs CAP_SYS_ADMIN and friends to run.
func (c *Client) RunDetached(spec NodeSpec) error {
	args := []string{"run", "-d", "--name", spec.Name}
	if c.supportsCapAdd() {
		args = append(args, "--cap-add", "ALL")
	}
	if spec.CPUs != "" {
		args = append(args, "--cpus", spec.CPUs)
	}
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	args = append(args, spec.Image)
	_, err := run(args...)
	return err
}

func (c *Client) supportsCapAdd() bool {
	c.capAddOnce.Do(func() {
		out, _ := run("run", "--help")
		c.capAdd = strings.Contains(out, "--cap-add")
	})
	return c.capAdd
}

// Exec runs a command inside a node and returns combined output.
func (c *Client) Exec(node string, command ...string) (string, error) {
	return run(append([]string{"exec", node}, command...)...)
}

// ExecStdin runs a command inside a node with r piped to the process's stdin.
func (c *Client) ExecStdin(node string, r io.Reader, command ...string) error {
	args := append([]string{"exec", "-i", node}, command...)
	cmd := exec.Command(Bin, args...)
	cmd.Stdin = r
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &CmdError{Args: args, Output: string(out), Err: err}
	}
	return nil
}

// WaitReady polls until the guest's containerd answers, meaning systemd
// finished booting and the kubelet's runtime is available.
func (c *Client) WaitReady(node string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		out, err := c.Exec(node, "systemctl", "is-active", "containerd")
		if err == nil && strings.TrimSpace(out) == "active" {
			return nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		return fmt.Errorf("node %s never became ready: %w", node, lastErr)
	}
	return fmt.Errorf("node %s never became ready within %s", node, timeout)
}

var ipv4Re = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)

// IP returns the node's routable IPv4 address, asking the guest for the
// source address it would use to reach the internet. That's stable across
// `container inspect`'s differing JSON shapes between CLI versions.
func (c *Client) IP(node string) (string, error) {
	out, err := c.Exec(node, "sh", "-c",
		`ip -4 route get 1.1.1.1 | awk '{for(i=1;i<NF;i++) if ($i=="src") print $(i+1)}'`)
	if err == nil {
		if ip := strings.TrimSpace(out); ipv4Re.MatchString(ip) {
			return ip, nil
		}
	}
	raw, ierr := run("inspect", node)
	if ierr != nil {
		if err != nil {
			return "", err
		}
		return "", ierr
	}
	m := regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+)(?:/\d+)?`).FindStringSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("could not determine IP address of node %s", node)
	}
	return m[1], nil
}

// Node is one row from `container list`.
type Node struct {
	Name  string
	Image string
	State string
}

// List returns containers (running or stopped) whose name starts with prefix.
func List(prefix string) ([]Node, error) {
	out, err := run("list", "-a", "--format", "json")
	if err != nil {
		return nil, err
	}
	return parseList(out, prefix)
}

// parseList tolerates the loosely-typed JSON container CLI emits, digging
// out only the fields this tool needs.
func parseList(out, prefix string) ([]Node, error) {
	var rows []map[string]any
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, fmt.Errorf("parsing container list output: %w", err)
	}
	var nodes []Node
	for _, row := range rows {
		name := firstString(row, "configuration.id", "id", "name")
		if name == "" || !strings.HasPrefix(name, prefix) {
			continue
		}
		nodes = append(nodes, Node{
			Name:  name,
			Image: firstString(row, "configuration.image.reference", "image", "imageRef"),
			State: firstString(row, "status.state", "status", "state"),
		})
	}
	return nodes, nil
}

func firstString(m map[string]any, paths ...string) string {
	for _, p := range paths {
		var cur any = m
		ok := true
		for _, key := range strings.Split(p, ".") {
			node, isMap := cur.(map[string]any)
			if !isMap {
				ok = false
				break
			}
			cur, ok = node[key]
			if !ok {
				break
			}
		}
		if ok {
			if s, isStr := cur.(string); isStr && s != "" {
				return s
			}
		}
	}
	return ""
}

// Remove force-deletes containers, ignoring not-found errors.
func Remove(names ...string) error {
	if len(names) == 0 {
		return nil
	}
	out, err := run(append([]string{"delete", "--force"}, names...)...)
	if err != nil && !strings.Contains(strings.ToLower(out), "not found") {
		return err
	}
	return nil
}

// ImagePull pulls a node image so `run` starts instantly afterwards.
func ImagePull(image string) error {
	_, err := run("image", "pull", image)
	return err
}
