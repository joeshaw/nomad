package command

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/mitchellh/colorstring"

	"github.com/hashicorp/nomad/api"
)

type NodeStatusCommand struct {
	Meta
	color *colorstring.Colorize
}

func (c *NodeStatusCommand) Help() string {
	helpText := `
Usage: nomad node-status [options] <node>

  Display status information about a given node. The list of nodes
  returned includes only nodes which jobs may be scheduled to, and
  includes status and other high-level information.

  If a node ID is passed, information for that specific node will
  be displayed. If no node ID's are passed, then a short-hand
  list of all nodes will be displayed. The -self flag is useful to
  quickly access the status of the local node.

General Options:

  ` + generalOptionsUsage() + `

Node Status Options:

  -short
    Display short output. Used only when a single node is being
    queried, and drops verbose output about node allocations.

  -verbose
    Display full information.

  -stats 
    Display detailed resource usage statistics

  -self
    Query the status of the local node.

  -allocs
    Display a count of running allocations for each node.
`
	return strings.TrimSpace(helpText)
}

func (c *NodeStatusCommand) Synopsis() string {
	return "Display status information about nodes"
}

func (c *NodeStatusCommand) Run(args []string) int {
	var short, verbose, list_allocs, self, stats bool
	var hostStats *api.HostStats

	flags := c.Meta.FlagSet("node-status", FlagSetClient)
	flags.Usage = func() { c.Ui.Output(c.Help()) }
	flags.BoolVar(&short, "short", false, "")
	flags.BoolVar(&verbose, "verbose", false, "")
	flags.BoolVar(&list_allocs, "allocs", false, "")
	flags.BoolVar(&self, "self", false, "")
	flags.BoolVar(&stats, "stats", false, "")

	if err := flags.Parse(args); err != nil {
		return 1
	}

	// Check that we got either a single node or none
	args = flags.Args()
	if len(args) > 1 {
		c.Ui.Error(c.Help())
		return 1
	}

	// Truncate the id unless full length is requested
	length := shortId
	if verbose {
		length = fullId
	}

	// Get the HTTP client
	client, err := c.Meta.Client()
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Error initializing client: %s", err))
		return 1
	}

	// Use list mode if no node name was provided
	if len(args) == 0 && !self {
		// Query the node info
		nodes, _, err := client.Nodes().List(nil)
		if err != nil {
			c.Ui.Error(fmt.Sprintf("Error querying node status: %s", err))
			return 1
		}

		// Return nothing if no nodes found
		if len(nodes) == 0 {
			return 0
		}

		// Format the nodes list
		out := make([]string, len(nodes)+1)
		if list_allocs {
			out[0] = "ID|DC|Name|Class|Drain|Status|Running Allocs"
		} else {
			out[0] = "ID|DC|Name|Class|Drain|Status"
		}
		for i, node := range nodes {
			if list_allocs {
				numAllocs, err := getRunningAllocs(client, node.ID)
				if err != nil {
					c.Ui.Error(fmt.Sprintf("Error querying node allocations: %s", err))
					return 1
				}
				out[i+1] = fmt.Sprintf("%s|%s|%s|%s|%v|%s|%v",
					limit(node.ID, length),
					node.Datacenter,
					node.Name,
					node.NodeClass,
					node.Drain,
					node.Status,
					len(numAllocs))
			} else {
				out[i+1] = fmt.Sprintf("%s|%s|%s|%s|%v|%s",
					limit(node.ID, length),
					node.Datacenter,
					node.Name,
					node.NodeClass,
					node.Drain,
					node.Status)
			}
		}

		// Dump the output
		c.Ui.Output(formatList(out))
		return 0
	}

	// Query the specific node
	nodeID := ""
	if !self {
		nodeID = args[0]
	} else {
		var err error
		if nodeID, err = getLocalNodeID(client); err != nil {
			c.Ui.Error(err.Error())
			return 1
		}
	}
	if len(nodeID) == 1 {
		c.Ui.Error(fmt.Sprintf("Identifier must contain at least two characters."))
		return 1
	}
	if len(nodeID)%2 == 1 {
		// Identifiers must be of even length, so we strip off the last byte
		// to provide a consistent user experience.
		nodeID = nodeID[:len(nodeID)-1]
	}

	nodes, _, err := client.Nodes().PrefixList(nodeID)
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Error querying node info: %s", err))
		return 1
	}
	// Return error if no nodes are found
	if len(nodes) == 0 {
		c.Ui.Error(fmt.Sprintf("No node(s) with prefix %q found", nodeID))
		return 1
	}
	if len(nodes) > 1 {
		// Format the nodes list that matches the prefix so that the user
		// can create a more specific request
		out := make([]string, len(nodes)+1)
		out[0] = "ID|DC|Name|Class|Drain|Status"
		for i, node := range nodes {
			out[i+1] = fmt.Sprintf("%s|%s|%s|%s|%v|%s",
				limit(node.ID, length),
				node.Datacenter,
				node.Name,
				node.NodeClass,
				node.Drain,
				node.Status)
		}
		// Dump the output
		c.Ui.Output(fmt.Sprintf("Prefix matched multiple nodes\n\n%s", formatList(out)))
		return 0
	}
	// Prefix lookup matched a single node
	node, _, err := client.Nodes().Info(nodes[0].ID, nil)
	if err != nil {
		c.Ui.Error(fmt.Sprintf("Error querying node info: %s", err))
		return 1
	}

	if hostStats, err = client.Nodes().Stats(node.ID, nil); err != nil {
		c.Ui.Error(fmt.Sprintf("error fetching node resource utilization stats: %#v", err))
	}

	// Format the output
	basic := []string{
		fmt.Sprintf("[bold]Node ID[reset]|%s", limit(node.ID, length)),
		fmt.Sprintf("Name|%s", node.Name),
		fmt.Sprintf("Class|%s", node.NodeClass),
		fmt.Sprintf("DC|%s", node.Datacenter),
		fmt.Sprintf("Drain|%v", node.Drain),
		fmt.Sprintf("Status|%s", node.Status),
	}
	if hostStats != nil {
		uptime := time.Duration(hostStats.Uptime * uint64(time.Second))
		basic = append(basic, fmt.Sprintf("Uptime|%s", uptime.String()))
	}
	c.Ui.Output(c.Colorize().Color(formatKV(basic)))

	if !short {
		resources, err := getResources(client, node)
		if err != nil {
			c.Ui.Error(fmt.Sprintf("Error querying node resources: %s", err))
			return 1
		}
		c.Ui.Output(c.Colorize().Color("\n[bold]==> Resource Utilization (Actual)[reset]"))
		c.Ui.Output(formatList(resources))
		if hostStats != nil && stats {
			c.Ui.Output(c.Colorize().Color("\n===> [bold]Detailed CPU Stats[reset]"))
			c.printCpuStats(hostStats)
			c.Ui.Output(c.Colorize().Color("\n===> [bold]Detailed Memory Stats[reset]"))
			c.printMemoryStats(hostStats)
			c.Ui.Output(c.Colorize().Color("\n===> [bold]Detailed Disk Stats[reset]"))
			c.printDiskStats(hostStats)
		}

		allocs, err := getAllocs(client, node, length)
		if err != nil {
			c.Ui.Error(fmt.Sprintf("Error querying node allocations: %s", err))
			return 1
		}

		if len(allocs) > 1 {
			c.Ui.Output("\n==> Allocations")
			c.Ui.Output(formatList(allocs))
		}
	}

	if verbose {
		// Print the attributes
		keys := make([]string, len(node.Attributes))
		for k := range node.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var attributes []string
		for _, k := range keys {
			if k != "" {
				attributes = append(attributes, fmt.Sprintf("%s|%s", k, node.Attributes[k]))
			}
		}
		c.Ui.Output("\n==> Attributes")
		c.Ui.Output(formatKV(attributes))
	}

	return 0
}

func (c *NodeStatusCommand) printCpuStats(hostStats *api.HostStats) {
	for _, cpuStat := range hostStats.CPU {
		cpuStatsAttr := make([]string, 4)
		cpuStatsAttr[0] = fmt.Sprintf("CPU|%v", cpuStat.CPU)
		cpuStatsAttr[1] = fmt.Sprintf("User|%v", formatFloat64(cpuStat.User))
		cpuStatsAttr[2] = fmt.Sprintf("System|%v", formatFloat64(cpuStat.System))
		cpuStatsAttr[3] = fmt.Sprintf("Idle|%v", formatFloat64(cpuStat.Idle))
		c.Ui.Output(formatKV(cpuStatsAttr))
		c.Ui.Output("")
	}
}

func (c *NodeStatusCommand) printMemoryStats(hostStats *api.HostStats) {
	memoryStat := hostStats.Memory
	memStatsAttr := make([]string, 4)
	memStatsAttr[0] = fmt.Sprintf("Total|%v", humanize.Bytes(memoryStat.Total))
	memStatsAttr[1] = fmt.Sprintf("Available|%v", humanize.Bytes(memoryStat.Available))
	memStatsAttr[2] = fmt.Sprintf("Used|%v", humanize.Bytes(memoryStat.Used))
	memStatsAttr[3] = fmt.Sprintf("Free|%v", humanize.Bytes(memoryStat.Free))
	c.Ui.Output(formatKV(memStatsAttr))
}

func (c *NodeStatusCommand) printDiskStats(hostStats *api.HostStats) {
	for _, diskStat := range hostStats.DiskStats {
		diskStatsAttr := make([]string, 6)
		diskStatsAttr[0] = fmt.Sprintf("Device|%s", diskStat.Device)
		diskStatsAttr[1] = fmt.Sprintf("MountPoint|%s", diskStat.Mountpoint)
		diskStatsAttr[2] = fmt.Sprintf("Size|%s", humanize.Bytes(diskStat.Size))
		diskStatsAttr[3] = fmt.Sprintf("Used|%s", humanize.Bytes(diskStat.Used))
		diskStatsAttr[4] = fmt.Sprintf("Available|%s", humanize.Bytes(diskStat.Available))
		diskStatsAttr[5] = fmt.Sprintf("Used Percent|%s", formatFloat64(diskStat.UsedPercent))
		c.Ui.Output(formatKV(diskStatsAttr))
		c.Ui.Output("")
	}
}

// getRunningAllocs returns a slice of allocation id's running on the node
func getRunningAllocs(client *api.Client, nodeID string) ([]*api.Allocation, error) {
	var allocs []*api.Allocation

	// Query the node allocations
	nodeAllocs, _, err := client.Nodes().Allocations(nodeID, nil)
	// Filter list to only running allocations
	for _, alloc := range nodeAllocs {
		if alloc.ClientStatus == "running" {
			allocs = append(allocs, alloc)
		}
	}
	return allocs, err
}

// getAllocs returns information about every running allocation on the node
func getAllocs(client *api.Client, node *api.Node, length int) ([]string, error) {
	var allocs []string
	// Query the node allocations
	nodeAllocs, _, err := client.Nodes().Allocations(node.ID, nil)
	// Format the allocations
	allocs = make([]string, len(nodeAllocs)+1)
	allocs[0] = "ID|Eval ID|Job ID|Task Group|Desired Status|Client Status"
	for i, alloc := range nodeAllocs {
		allocs[i+1] = fmt.Sprintf("%s|%s|%s|%s|%s|%s",
			limit(alloc.ID, length),
			limit(alloc.EvalID, length),
			alloc.JobID,
			alloc.TaskGroup,
			alloc.DesiredStatus,
			alloc.ClientStatus)
	}
	return allocs, err
}

// getResources returns the resource usage of the node.
func getResources(client *api.Client, node *api.Node) ([]string, error) {
	var resources []string
	var cpu, mem, disk, iops int
	var totalCpu, totalMem, totalDisk, totalIops int

	// Compute the total
	r := node.Resources
	res := node.Reserved
	if res == nil {
		res = &api.Resources{}
	}
	totalCpu = r.CPU - res.CPU
	totalMem = r.MemoryMB - res.MemoryMB
	totalDisk = r.DiskMB - res.DiskMB
	totalIops = r.IOPS - res.IOPS

	// Get list of running allocations on the node
	runningAllocs, err := getRunningAllocs(client, node.ID)

	// Get Resources
	for _, alloc := range runningAllocs {
		cpu += alloc.Resources.CPU
		mem += alloc.Resources.MemoryMB
		disk += alloc.Resources.DiskMB
		iops += alloc.Resources.IOPS
	}

	resources = make([]string, 2)
	resources[0] = "CPU|Memory MB|Disk MB|IOPS"
	resources[1] = fmt.Sprintf("%v/%v|%v/%v|%v/%v|%v/%v",
		cpu,
		totalCpu,
		mem,
		totalMem,
		disk,
		totalDisk,
		iops,
		totalIops)

	return resources, err
}

func formatFloat64(val float64) string {
	return strconv.FormatFloat(val, 'f', 2, 64)
}
