package maelstrom

import (
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/stretchr/testify/assert"
	"math/rand"
	"reflect"
	"strconv"
	"testing"
	"testing/quick"
	"time"
)

var defaultRand = rand.New(rand.NewSource(time.Now().UnixNano()))

const ramRequiredMedium = 1000
const componentA = "a"

func randActivity(rand *rand.Rand) v1.ComponentActivity {
	return v1.ComponentActivity{
		Requests:    rand.Int63n(100000),
		Concurrency: rand.Float64(),
	}
}

func randActivityList(rand *rand.Rand) []v1.ComponentActivity {
	count := rand.Intn(6)
	activity := make([]v1.ComponentActivity, count)
	for i := 0; i < count; i++ {
		activity[i] = randActivity(rand)
	}
	return activity
}

func randComponent(componentName string, maxReserveRam int64, rand *rand.Rand) v1.Component {
	minInst := rand.Int63n(5)
	scaleDownPct := rand.Float64() / 3
	return v1.Component{
		Name:                    componentName,
		ProjectName:             "",
		Environment:             nil,
		MinInstances:            minInst,
		MaxInstances:            minInst + rand.Int63n(50),
		MaxConcurrency:          rand.Int63n(5) + 1,
		ScaleDownConcurrencyPct: scaleDownPct,
		ScaleUpConcurrencyPct:   scaleDownPct + rand.Float64(),
		MaxDurationSeconds:      rand.Int63n(300) + 1,
		Version:                 rand.Int63n(500),
		ModifiedAt:              common.TimeToMillis(time.Now()) - rand.Int63n(9999999),
		Docker: &v1.DockerComponent{
			ReserveMemoryMiB:   rand.Int63n(maxReserveRam) + 1,
			IdleTimeoutSeconds: rand.Int63n(300) + 1,
		},
	}
}

func randComponentInfoWithName(componentName string, reserveMiB int64, rand *rand.Rand) v1.ComponentInfo {
	return v1.ComponentInfo{
		ComponentName:     componentName,
		MemoryReservedMiB: reserveMiB,
		LastRequestTime:   common.TimeToMillis(time.Now().Add(-1 * time.Second * time.Duration(rand.Intn(3600)))),
		Activity:          randActivityList(rand),
	}
}

func randComponentInfo(componentNum int, maxRam int64, rand *rand.Rand) v1.ComponentInfo {
	reserveMiB := (128 * rand.Int63n(16)) + 128
	if reserveMiB > maxRam {
		reserveMiB = maxRam
	}
	return randComponentInfoWithName(strconv.Itoa(componentNum), reserveMiB, rand)
}

func randComponentInfos(maxComponents int, totalMemoryMiB int64, rand *rand.Rand) []v1.ComponentInfo {
	comps := make([]v1.ComponentInfo, 0)
	memoryAvail := totalMemoryMiB
	for i := 0; i < maxComponents && memoryAvail > 0; i++ {
		comp := randComponentInfo(i, memoryAvail, rand)
		memoryAvail -= comp.MemoryReservedMiB
		comps = append(comps, comp)
	}
	return comps
}

func randComponentInfosFromComponents(maxComponents int, components []v1.Component,
	totalMemoryMiB int64, rand *rand.Rand) []v1.ComponentInfo {

	maxComps := rand.Intn(maxComponents)
	if maxComps > len(components) {
		maxComps = len(components)
	}
	comps := make([]v1.ComponentInfo, 0)
	memoryAvail := totalMemoryMiB

	rand.Shuffle(len(components), func(i, j int) {
		components[i], components[j] = components[j], components[i]
	})

	for i := 0; i < maxComps && memoryAvail > 0; i++ {
		comp := randComponentInfoWithName(components[i].Name, components[i].Docker.ReserveMemoryMiB, rand)
		if memoryAvail >= comp.MemoryReservedMiB {
			memoryAvail -= comp.MemoryReservedMiB
			comps = append(comps, comp)
		}
	}
	return comps
}

func randNodeNoComponent(nodeNum int, maxRam int64, rand *rand.Rand) v1.NodeStatus {
	totalMemoryMiB := rand.Int63n(16000) + 128
	if totalMemoryMiB > maxRam {
		totalMemoryMiB = maxRam
	}
	return v1.NodeStatus{
		NodeId:            fmt.Sprintf("node-%d", nodeNum),
		StartedAt:         common.TimeToMillis(time.Now()) - rand.Int63n(100000),
		ObservedAt:        common.TimeToMillis(time.Now()),
		Version:           rand.Int63n(1000000) + 1,
		PeerUrl:           fmt.Sprintf("http://%d.example.org/", nodeNum),
		TotalMemoryMiB:    totalMemoryMiB,
		FreeMemoryMiB:     0,
		NumCPUs:           0,
		LoadAvg1m:         float64(rand.Intn(99999999)),
		LoadAvg5m:         0,
		LoadAvg15m:        0,
		RunningComponents: nil,
	}
}

func randNodeWithComponents(nodeNum int, maxComponents int, maxRam int64, rand *rand.Rand) v1.NodeStatus {
	node := randNodeNoComponent(nodeNum, maxRam, rand)
	node.RunningComponents = randComponentInfos(maxComponents, node.TotalMemoryMiB, rand)
	return node
}

func randNodeUsingComponents(nodeNum int, maxComponents int, maxRam int64, components []v1.Component,
	rand *rand.Rand) v1.NodeStatus {
	node := randNodeNoComponent(nodeNum, maxRam, rand)
	node.RunningComponents = randComponentInfosFromComponents(maxComponents, components, node.TotalMemoryMiB, rand)
	return node
}

func randNodes(count int, rand *rand.Rand) []v1.NodeStatus {
	nodes := make([]v1.NodeStatus, count)
	for i := 0; i < count; i++ {
		nodes[i] = randNodeWithComponents(i, rand.Intn(25), 16000, rand)
	}
	return nodes
}

func randNode(nodeNum int, rand *rand.Rand) v1.NodeStatus {
	return randNodeWithComponents(nodeNum, rand.Intn(25), 16000, rand)
}

type NodeList []v1.NodeStatus

func (n NodeList) Generate(rand *rand.Rand, size int) reflect.Value {
	nodes := make([]v1.NodeStatus, size+1)
	nodes[0] = randNode(0, rand)
	nodes[0].TotalMemoryMiB = ramRequiredMedium * 2
	for i := 1; i <= size; i++ {
		nodes[i] = randNode(i, rand)
	}
	return reflect.ValueOf(nodes)
}

type FullNodeList []v1.NodeStatus

func (n FullNodeList) Generate(rand *rand.Rand, size int) reflect.Value {
	nodes := make([]v1.NodeStatus, rand.Intn(size*2)+1)
	for i := 0; i < len(nodes); i++ {
		nodes[i] = randNodeWithComponents(i, 30, ramRequiredMedium, rand)
	}
	return reflect.ValueOf(nodes)
}

type NodesAndComponents struct {
	Input CalcAutoscaleInput
}

func (n NodesAndComponents) Generate(rand *rand.Rand, size int) reflect.Value {
	nodes := make([]v1.NodeStatus, size)
	components := make([]v1.Component, rand.Intn(30)+1)

	for i := 0; i < len(components); i++ {
		components[i] = randComponent(fmt.Sprintf("comp-%d", i), 2000, rand)
	}

	for i := 0; i < len(nodes); i++ {
		nodes[i] = randNodeUsingComponents(i, 10, 2000, components, rand)
	}

	compByName := componentsByName(components)

	for _, node := range nodes {
		totalRam, _ := totalRamUsed(node, nil, compByName)
		if totalRam > node.TotalMemoryMiB {
			panic(fmt.Sprintf("totalRam %d > %d", totalRam, node.TotalMemoryMiB))
		}
	}

	return reflect.ValueOf(NodesAndComponents{Input: CalcAutoscaleInput{
		Nodes:            nodes,
		ComponentsByName: compByName,
	}})
}

func scaleDownCount(counts []v1.ComponentDelta) int {
	count := 0
	for _, c := range counts {
		if c.Delta < 0 {
			count++
		}
	}
	return count
}

/////////////////////////////////////////////////////////////////////////////

func TestBestStartComponentOptionNeverReturnsNilIfNodeHasEnoughRAM(t *testing.T) {
	f := func(nl NodeList) bool {
		return BestStartComponentOption(nl, map[string]*PlacementOption{}, componentA, ramRequiredMedium, true) != nil
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

func TestBestStartComponentOptionNeverStopsContainerIfNodeHasEnoughUnreservedRAM(t *testing.T) {
	f := func(nl FullNodeList) bool {
		// make sure one node has enough free ram to avoid stopping a container
		if len(nl[0].RunningComponents) > 0 {
			nl[0].TotalMemoryMiB = ramRequiredMedium + 50
			nl[0].RunningComponents = nl[0].RunningComponents[0:1]
			nl[0].RunningComponents[0].MemoryReservedMiB = 10
		}
		option := BestStartComponentOption(nl, map[string]*PlacementOption{}, componentA, ramRequiredMedium, true)
		return option != nil && scaleDownCount(option.Input.TargetCounts) == 0
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

func TestBestStartComponentOptionNoNodeWithEnoughRam(t *testing.T) {
	nodes := []v1.NodeStatus{randNode(0, defaultRand)}
	nodes[0].TotalMemoryMiB = ramRequiredMedium - 1
	assert.Nil(t, BestStartComponentOption(nodes, map[string]*PlacementOption{}, componentA, ramRequiredMedium, true))
}

func TestBestStartComponentOptionSingleNode(t *testing.T) {
	nodes := []v1.NodeStatus{randNode(0, defaultRand)}
	nodes[0].TotalMemoryMiB += ramRequiredMedium
	option := BestStartComponentOption(nodes, map[string]*PlacementOption{}, componentA, ramRequiredMedium, true)
	assert.Equal(t, nodes[0], option.TargetNode)
}

func TestBestStartComponentOptionStopsExisting(t *testing.T) {
	nodes := []v1.NodeStatus{randNode(0, defaultRand)}
	nodes[0].TotalMemoryMiB = ramRequiredMedium + 100
	nodes[0].RunningComponents = []v1.ComponentInfo{
		{
			ComponentName:     "0",
			MemoryReservedMiB: 500,
			LastRequestTime:   0,
		},
		{
			ComponentName:     "1",
			MemoryReservedMiB: 400,
			LastRequestTime:   0,
		},
	}
	expected := &PlacementOption{
		TargetNode: nodes[0],
		Input: v1.StartStopComponentsInput{
			ClientNodeId:  "",
			TargetVersion: nodes[0].Version,
			TargetCounts: []v1.ComponentDelta{
				{ComponentName: "0", Delta: -1},
				{ComponentName: "1", Delta: -1},
				{ComponentName: componentA, Delta: 1, RequiredMemoryMiB: ramRequiredMedium},
			},
			ReturnStatus: true,
		},
	}
	assert.Equal(t, expected, BestStartComponentOption(nodes, map[string]*PlacementOption{}, componentA, ramRequiredMedium, true))
}

func TestBestStartComponentOptionStopsComponentWithMostInstances(t *testing.T) {
	componentARam := int64(300)
	nodes := []v1.NodeStatus{randNode(0, defaultRand), randNode(1, defaultRand)}
	nodes[0].TotalMemoryMiB = ramRequiredMedium + 100
	nodes[0].LoadAvg1m = 1
	nodes[0].RunningComponents = []v1.ComponentInfo{
		{
			ComponentName:     "0",
			MemoryReservedMiB: 500,
			LastRequestTime:   0,
		},
		{
			ComponentName:     "1",
			MemoryReservedMiB: 400,
			LastRequestTime:   0,
		},
	}
	nodes[1].TotalMemoryMiB = ramRequiredMedium + 100
	nodes[1].LoadAvg1m = .5
	nodes[1].RunningComponents = []v1.ComponentInfo{
		{
			ComponentName:     "1",
			MemoryReservedMiB: 900,
			LastRequestTime:   0,
		},
	}
	expected := &PlacementOption{
		TargetNode: nodes[1],
		Input: v1.StartStopComponentsInput{
			ClientNodeId:  "",
			TargetVersion: nodes[1].Version,
			TargetCounts: []v1.ComponentDelta{
				{ComponentName: "1", Delta: -1},
				{ComponentName: componentA, Delta: 1, RequiredMemoryMiB: componentARam},
			},
			ReturnStatus: true,
		},
	}
	assert.Equal(t, expected, BestStartComponentOption(nodes, map[string]*PlacementOption{}, componentA, componentARam, true))
}
