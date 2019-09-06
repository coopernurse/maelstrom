package gateway

import (
	"fmt"
	log "github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"math"
	"sort"
)

type CalcAutoscaleInput struct {
	Nodes            []v1.NodeStatus
	ComponentsByName map[string]v1.Component
	MinConcur        float64
	MaxConcur        float64
}

type componentConcurrency struct {
	componentName     string
	minInstances      int
	maxInstances      int
	currentInstances  int
	targetInstances   int
	reserveMemoryMiB  int64
	pctMaxConcurrency float64
}

type componentDelta struct {
	componentName    string
	reserveMemoryMiB int64
	delta            int
}

type componentDeltaByDelta []componentDelta

func (s componentDeltaByDelta) Len() int           { return len(s) }
func (s componentDeltaByDelta) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s componentDeltaByDelta) Less(i, j int) bool { return s[i].delta < s[j].delta }

func componentsByName(comps []v1.Component) map[string]v1.Component {
	byName := map[string]v1.Component{}
	for _, c := range comps {
		byName[c.Name] = c
	}
	return byName
}

func loadActiveComponents(nodes []v1.NodeStatus, db v1.Db) (map[string]v1.Component, error) {
	activeComponents := map[string]bool{}
	for _, node := range nodes {
		for _, comp := range node.RunningComponents {
			activeComponents[comp.ComponentName] = true
		}
	}

	componentsByName := map[string]v1.Component{}
	listInput := v1.ListComponentsInput{
		Limit:     1000,
		NextToken: "",
	}
	for {
		output, err := db.ListComponents(listInput)
		if err != nil {
			return nil, errors.Wrap(err, "scale: ListComponents failed")
		}
		for _, comp := range output.Components {
			if comp.MinInstances > 0 || activeComponents[comp.Name] {
				componentsByName[comp.Name] = comp
			}
		}
		listInput.NextToken = output.NextToken
		if listInput.NextToken == "" {
			break
		}
	}
	return componentsByName, nil
}

func toComponentConcurrency(nodes []v1.NodeStatus, componentsByName map[string]v1.Component,
	minConcurPct float64, maxConcurPct float64) []componentConcurrency {
	compInfoByComponent := map[string][]v1.ComponentInfo{}
	lastReqTimeByComponent := map[string]int64{}
	for _, node := range nodes {
		for _, compInfo := range node.RunningComponents {
			arr := compInfoByComponent[compInfo.ComponentName]
			compInfoByComponent[compInfo.ComponentName] = append(arr, compInfo)
			if compInfo.LastRequestTime > lastReqTimeByComponent[compInfo.ComponentName] {
				lastReqTimeByComponent[compInfo.ComponentName] = compInfo.LastRequestTime
			}
		}
	}

	var concur []componentConcurrency
	for compName, comp := range componentsByName {
		infos := compInfoByComponent[compName]
		pctMaxConcur := calcPctMaxConcurrency(infos, comp.MaxConcurrency)
		secsSinceLastReq := (common.NowMillis() - lastReqTimeByComponent[compName]) / 1000
		minInstances := comp.MinInstances
		if minInstances < 1 && secsSinceLastReq < comp.Docker.IdleTimeoutSeconds {
			minInstances = 1
		}
		concur = append(concur, componentConcurrency{
			componentName:    compName,
			minInstances:     int(comp.MinInstances),
			maxInstances:     int(comp.MaxInstances),
			reserveMemoryMiB: comp.Docker.ReserveMemoryMiB,
			currentInstances: len(infos),
			targetInstances: calcTargetInstances(len(infos), minInstances, comp.MaxInstances,
				pctMaxConcur, minConcurPct, maxConcurPct),
			pctMaxConcurrency: pctMaxConcur,
		})
	}

	return concur
}

func calcPctMaxConcurrency(infos []v1.ComponentInfo, maxConcurrency int64) float64 {
	if len(infos) == 0 {
		return 0
	}

	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}

	var sumOfAverageConcur float64
	for _, compInfo := range infos {
		nodeActivity := compInfo.Activity
		if len(nodeActivity) > 0 {
			sum := 0.0
			for _, a := range nodeActivity {
				sum += a.Concurrency
			}
			sumOfAverageConcur += sum / float64(len(nodeActivity))
		}
	}
	return sumOfAverageConcur / (float64(len(infos)) * float64(maxConcurrency))
}

func calcTargetInstances(currentInstCount int, minInst int64, maxInst int64,
	currentConcurPct float64, minConcurPct float64, maxConcurPct float64) int {

	target := int64(currentInstCount)
	if currentConcurPct > maxConcurPct || currentConcurPct < minConcurPct {
		target = int64(math.Ceil(float64(currentInstCount) * (currentConcurPct / maxConcurPct)))
	}

	if target < minInst {
		target = minInst
	}
	if target > maxInst && maxInst > minInst {
		target = maxInst
	}
	return int(target)
}

func toComponentDeltas(concurrency []componentConcurrency) []componentDelta {
	var deltas []componentDelta
	for _, c := range concurrency {
		if c.targetInstances != c.currentInstances {
			deltas = append(deltas, componentDelta{
				componentName:    c.componentName,
				reserveMemoryMiB: c.reserveMemoryMiB,
				delta:            c.targetInstances - c.currentInstances,
			})
		}
	}
	return deltas
}

func mergeTargetCounts(a []v1.ComponentDelta, b []v1.ComponentDelta) []v1.ComponentDelta {
	byComponentName := map[string]v1.ComponentDelta{}
	for _, count := range a {
		byComponentName[count.ComponentName] = count
	}
	for _, count := range b {
		oldCount, ok := byComponentName[count.ComponentName]
		if ok {
			count.Delta += oldCount.Delta
			if count.RequiredMemoryMiB <= 0 {
				count.RequiredMemoryMiB = oldCount.RequiredMemoryMiB
			}
		}
		byComponentName[count.ComponentName] = count
	}

	merged := make([]v1.ComponentDelta, 0)
	for _, count := range byComponentName {
		if count.Delta != 0 {
			if count.RequiredMemoryMiB <= 0 && count.Delta > 0 {
				panic(fmt.Sprintf("Invalid ComponentDelta: %+v", count))
			}
			merged = append(merged, count)
		}
	}
	return merged
}

func mergeOption(optionByNode map[string]*PlacementOption, toMerge *PlacementOption) *PlacementOption {
	prevOption := optionByNode[toMerge.TargetNode.NodeId]
	if prevOption != nil {
		toMerge.Input.TargetCounts = mergeTargetCounts(toMerge.Input.TargetCounts,
			prevOption.Input.TargetCounts)
	}
	return toMerge
}

func countByComponent(componentName string, rcs []v1.ComponentInfo) int {
	count := 0
	for _, rc := range rcs {
		if rc.ComponentName == componentName {
			count++
		}
	}
	return count
}

func nodeRamUsedMiB(runningComps []v1.ComponentInfo) int64 {
	ramUsed := int64(0)
	for _, rc := range runningComps {
		ramUsed += rc.MemoryReservedMiB
	}
	return ramUsed
}

func toOptionsByNode(options []*PlacementOption) map[string]*PlacementOption {
	byNodeId := map[string]*PlacementOption{}
	for _, opt := range options {
		byNodeId[opt.TargetNode.NodeId] = opt
	}
	return byNodeId
}

func countByComponentFromRunningComp(nodes []v1.NodeStatus, optionByNode map[string]*PlacementOption) map[string]int {
	countByComponent := map[string]int{}
	for _, node := range nodes {
		for _, rc := range RunningComponents(node, optionByNode[node.NodeId]) {
			countByComponent[rc.ComponentName] += 1
		}
	}
	return countByComponent
}

func computeScaleStartStopInputs(nodes []v1.NodeStatus, deltas []componentDelta) []*PlacementOption {

	optionByNode := map[string]*PlacementOption{}
	beforeRamByNode := map[string]int64{}

	for _, node := range nodes {
		totalRam := int64(0)
		for _, rc := range node.RunningComponents {
			totalRam += rc.MemoryReservedMiB
		}
		beforeRamByNode[node.NodeId] = totalRam
	}

	sort.Sort(componentDeltaByDelta(deltas))

	// scale down components
	for _, d := range deltas {
		if d.delta < 0 {
			stopTotal := d.delta * -1
			for i := 0; i < stopTotal; i++ {
				option := BestStopComponentOption(nodes, optionByNode, d.componentName)
				if option == nil {
					log.Warn("scale: unable to scale down component", "component", d.componentName, "delta", d.delta)
					break
				} else {
					optionByNode[option.TargetNode.NodeId] = mergeOption(optionByNode, option)
				}
			}
		}
	}

	// scale up
	for _, d := range deltas {
		if d.delta > 0 {
			for i := 0; i < d.delta; i++ {
				option := BestStartComponentOption(nodes, optionByNode, d.componentName, d.reserveMemoryMiB, false)
				if option == nil {
					log.Warn("scale: unable to scale up component", "component", d.componentName, "delta", d.delta)
					break
				} else {
					optionByNode[option.TargetNode.NodeId] = mergeOption(optionByNode, option)
				}
			}
		}
	}

	// rebalance - migrate components to empty nodes
	for _, node := range nodes {
		runningComps := RunningComponents(node, optionByNode[node.NodeId])
		ramUsed := nodeRamUsedMiB(runningComps)
		if ramUsed == 0 {
			fromNode, comp := findCompToMove(nodes, optionByNode, node.NodeId, node.TotalMemoryMiB)
			if fromNode != nil {
				fromOpt := getOrCreate(optionByNode, *fromNode)
				fromOpt.Input.TargetCounts = mergeTargetCounts(fromOpt.Input.TargetCounts, []v1.ComponentDelta{
					{
						ComponentName: comp.ComponentName,
						Delta:         -1,
					},
				})
				optionByNode[fromNode.NodeId] = fromOpt

				toOpt := getOrCreate(optionByNode, node)
				toOpt.Input.TargetCounts = mergeTargetCounts(toOpt.Input.TargetCounts, []v1.ComponentDelta{
					{
						ComponentName:     comp.ComponentName,
						Delta:             1,
						RequiredMemoryMiB: comp.MemoryReservedMiB,
					},
				})
				optionByNode[node.NodeId] = toOpt
			}
		}
	}

	options := make([]*PlacementOption, 0)
	for _, opt := range optionByNode {
		if len(opt.Input.TargetCounts) > 0 {
			options = append(options, opt)
		}
	}
	sort.Sort(PlacementOptionByNode(options))
	return options
}

func getOrCreate(optionByNode map[string]*PlacementOption, node v1.NodeStatus) *PlacementOption {
	opt := optionByNode[node.NodeId]
	if opt == nil {
		opt = &PlacementOption{
			TargetNode: node,
			Input: v1.StartStopComponentsInput{
				ClientNodeId:  "",
				TargetVersion: node.Version,
				TargetCounts:  []v1.ComponentDelta{},
				ReturnStatus:  true,
			},
		}
	}
	return opt
}

func findCompToMove(nodes []v1.NodeStatus, placementByNode map[string]*PlacementOption, otherNodeId string,
	freeMemoryMiB int64) (*v1.NodeStatus, *v1.ComponentInfo) {
	for _, n := range nodes {
		runningComps := RunningComponents(n, placementByNode[n.NodeId])
		if n.NodeId != otherNodeId && len(runningComps) > 1 {
			for _, rc := range runningComps {
				if rc.MemoryReservedMiB <= freeMemoryMiB {
					return &n, &rc
				}
			}
		}
	}
	return nil, nil
}

func CalcAutoscalePlacement(nodes []v1.NodeStatus, componentsByName map[string]v1.Component,
	minConcurPct float64, maxConcurPct float64) []*PlacementOption {
	concurrency := toComponentConcurrency(nodes, componentsByName, minConcurPct, maxConcurPct)
	deltas := toComponentDeltas(concurrency)
	return computeScaleStartStopInputs(nodes, deltas)
}

func OptionNetRam(option *PlacementOption, componentsByName map[string]v1.Component) int64 {
	ram := int64(0)
	for _, tc := range option.Input.TargetCounts {
		ram += tc.Delta * componentsByName[tc.ComponentName].Docker.ReserveMemoryMiB
	}
	return ram
}

func totalRamUsed(node v1.NodeStatus, options []PlacementOption,
	componentsByName map[string]v1.Component) (int64, *PlacementOption) {
	totalRam := int64(0)
	optIdx := -1
	for _, rc := range node.RunningComponents {
		totalRam += rc.MemoryReservedMiB
	}
	for idx, opt := range options {
		if opt.TargetNode.NodeId == node.NodeId {
			optIdx = idx
			fmt.Printf("found opt: %v\n", opt)
			for _, tc := range opt.Input.TargetCounts {
				comp, ok := componentsByName[tc.ComponentName]
				if ok {
					totalRam += comp.Docker.ReserveMemoryMiB * tc.Delta
				} else {
					fmt.Printf("comp not found: %s - %+v\n", tc.ComponentName, tc)
				}
			}
		}
	}
	if optIdx >= 0 {
		return totalRam, &options[optIdx]
	}
	return totalRam, nil
}
