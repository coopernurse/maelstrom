package maelstrom

//
//import (
//	"context"
//	"encoding/base64"
//	"encoding/json"
//	"fmt"
//	"github.com/coopernurse/maelstrom/pkg/common"
//	"github.com/coopernurse/maelstrom/pkg/v1"
//	"github.com/docker/docker/api/types"
//	"github.com/docker/docker/api/types/container"
//	"github.com/docker/docker/api/types/filters"
//	"github.com/docker/docker/api/types/mount"
//	docker "github.com/docker/docker/client"
//	"github.com/docker/go-connections/nat"
//	"github.com/mgutz/logxi/v1"
//	"github.com/pkg/errors"
//	"io"
//	"io/ioutil"
//	"net/http"
//	"net/http/httptest"
//	"os"
//	"os/exec"
//	"strconv"
//	"strings"
//	"sync"
//	"sync/atomic"
//	"time"
//)
//
/////////////////////////////////////////////////////////////
//// DockerHandlerFactory //
////////////////////////////
//
//func NewDockerHandlerFactory(dockerClient *docker.Client, resolver ComponentResolver, db Db,
//	ctx context.Context, privatePort int) (*DockerHandlerFactory, error) {
//	containers, err := listContainers(dockerClient)
//	if err != nil {
//		return nil, err
//	}
//
//	maelstromHost, err := resolveMaelstromHost(dockerClient)
//	if err != nil {
//		return nil, err
//	}
//	maelstromUrl := fmt.Sprintf("http://%s:%d", maelstromHost, privatePort)
//	log.Info("handler: creating DockerHandlerFactory", "maelstromUrl", maelstromUrl)
//
//	byComponentName := map[string][]*Container{}
//	reqChanByComponent := map[string]chan *MaelRequest{}
//	for _, c := range containers {
//		name := c.Labels["maelstrom_component"]
//		verStr := c.Labels["maelstrom_version"]
//		if name != "" && verStr != "" {
//			comp, err := resolver.ByName(name)
//			if err == nil {
//				var currentImageId string
//				image, err := getImageByNameStripRepo(dockerClient, comp.Docker.Image)
//				if err == nil {
//					if image != nil {
//						currentImageId = image.ID
//					}
//				} else {
//					log.Error("handler: cannot list images", "component", name, "err", err)
//				}
//
//				if c.ImageID != currentImageId {
//					stopContainerLogErr(dockerClient, c.ID, name, verStr, "docker image modified")
//				} else if strconv.Itoa(int(comp.Version)) != verStr {
//					stopContainerLogErr(dockerClient, c.ID, name, verStr, "component version changed")
//				} else {
//					// happy path - running container matches docker image ID and component version
//					reqCh := reqChanByComponent[name]
//					if reqCh == nil {
//						reqCh = make(chan *MaelRequest)
//						reqChanByComponent[name] = reqCh
//					}
//					containerWrap, err := StartContainer(reqCh, dockerClient, comp, c.ID)
//					if err == nil {
//						list := byComponentName[name]
//						byComponentName[name] = append(list, containerWrap)
//					} else {
//						log.Error("handler: cannot create container wrapper", "component", name, "err", err)
//						stopContainerLogErr(dockerClient, c.ID, name, verStr, "container wrapper init failed")
//					}
//					log.Info("handler: added existing container", "component", name, "containerId", c.ID)
//				}
//			} else {
//				log.Warn("handler: cannot load component", "component", name, "err", err.Error())
//			}
//		}
//	}
//
//	return &DockerHandlerFactory{
//		dockerClient:       dockerClient,
//		db:                 db,
//		byComponentName:    byComponentName,
//		reqChanByComponent: reqChanByComponent,
//		maelstromUrl:       maelstromUrl,
//		ctx:                ctx,
//		version:            common.NowMillis(),
//		lock:               &sync.Mutex{},
//	}, nil
//}
//
//type DockerHandlerFactory struct {
//	dockerClient *docker.Client
//	//router          *Router
//	db                 Db
//	byComponentName    map[string][]*Container
//	reqChanByComponent map[string]chan *MaelRequest
//	maelstromUrl       string
//	ctx                context.Context
//	version            int64
//	lock               *sync.Mutex
//}
//
//func (f *DockerHandlerFactory) Version() int64 {
//	f.lock.Lock()
//	ver := f.version
//	f.lock.Unlock()
//	return ver
//}
//
//func (f *DockerHandlerFactory) IncrementVersion() int64 {
//	f.lock.Lock()
//	oldVer := f.version
//	f.version++
//	f.lock.Unlock()
//	return oldVer
//}
//
//func (f *DockerHandlerFactory) ReqChanByComponent(componentName string) chan *MaelRequest {
//	f.lock.Lock()
//	reqCh := f.reqChanByComponentLocked(componentName)
//	f.lock.Unlock()
//	return reqCh
//}
//
//func (f *DockerHandlerFactory) reqChanByComponentLocked(componentName string) chan *MaelRequest {
//	reqCh := f.reqChanByComponent[componentName]
//	if reqCh == nil {
//		reqCh = make(chan *MaelRequest)
//		f.reqChanByComponent[componentName] = reqCh
//	}
//	return reqCh
//}
//
//func (f *DockerHandlerFactory) HandlerComponentInfo() ([]v1.ComponentInfo, int64) {
//	f.lock.Lock()
//	defer f.lock.Unlock()
//	componentInfos := make([]v1.ComponentInfo, 0)
//	for _, clist := range f.byComponentName {
//		for _, cont := range clist {
//			componentInfos = append(componentInfos, cont.componentInfo())
//		}
//	}
//	return componentInfos, f.version
//}
//
//func (f *DockerHandlerFactory) ConvergeToTarget(target v1.ComponentDelta,
//	component v1.Component, async bool) (started int, stopped int, err error) {
//
//	reqCh := f.ReqChanByComponent(component.Name)
//
//	f.lock.Lock()
//	defer f.lock.Unlock()
//
//	delta := int(target.Delta)
//	containers := f.byComponentName[target.ComponentName]
//
//	// Count non-stopped handlers
//	currentCount := len(containers)
//	targetCount := currentCount + delta
//
//	// Bump internal version
//	f.version++
//
//	if delta < 0 {
//		// scale down
//		stopCount := delta * -1
//		for i := 0; i < stopCount; i++ {
//			idx := currentCount - i - 1
//			if async {
//				go containers[idx].Stop("scale down")
//			} else {
//				containers[idx].Stop("scale down")
//			}
//		}
//		f.byComponentName[target.ComponentName] = containers[:targetCount]
//	} else if delta > 0 {
//		// scale up
//		for i := 0; i < delta; i++ {
//			cont, containerId, err := f.startContainer(component, reqCh)
//			if err == nil {
//				containers = append(containers, cont)
//			} else {
//				log.Error("handler: unable to start container", "err", err.Error(), "component", component.Name)
//				f.stopContainerQuietly(containerId, component)
//			}
//		}
//		f.byComponentName[target.ComponentName] = containers
//	}
//
//	return
//}
//
//func (f *DockerHandlerFactory) stopContainerQuietly(containerId string, component v1.Component) {
//	if containerId != "" {
//		err := stopContainer(f.dockerClient, containerId, component.Name,
//			strconv.Itoa(int(component.Version)), "failed to start")
//		if err != nil {
//			log.Warn("handler: unable to stop container", "err", err.Error(), "component", component.Name)
//		}
//	}
//}
//
//func (f *DockerHandlerFactory) startContainer(component v1.Component, reqCh chan *MaelRequest) (*Container, string, error) {
//	err := pullImage(f.dockerClient, component)
//	if err != nil {
//		log.Warn("handler: unable to pull image", "err", err.Error(), "component", component.Name)
//	}
//
//	containerId, err := startContainer(f.dockerClient, component, f.maelstromUrl)
//	if err != nil {
//		return nil, containerId, err
//	}
//
//	c, err := StartContainer(reqCh, f.dockerClient, component, containerId)
//	if err != nil {
//		return nil, containerId, err
//	}
//
//	return c, containerId, nil
//}
//
//func (f *DockerHandlerFactory) GetComponentInfo(componentName string, containerId string) v1.ComponentInfo {
//	f.lock.Lock()
//	defer f.lock.Unlock()
//
//	containers, ok := f.byComponentName[componentName]
//	if ok {
//		for _, cont := range containers {
//			if cont.containerId == containerId {
//				return cont.componentInfo()
//			}
//		}
//	}
//	return v1.ComponentInfo{}
//}
//
//func (f *DockerHandlerFactory) OnDockerEvent(msg common.DockerEvent) {
//	if msg.ContainerExited != nil {
//		f.OnContainerExited(*msg.ContainerExited)
//	}
//	if msg.ImageUpdated != nil {
//		f.OnImageUpdated(*msg.ImageUpdated)
//	}
//}
//
//func (f *DockerHandlerFactory) OnContainerExited(msg common.ContainerExitedMessage) {
//	f.lock.Lock()
//	defer f.lock.Unlock()
//
//	for componentName, containers := range f.byComponentName {
//		removeIdx := -1
//		for idx, cont := range containers {
//			if cont.containerId == msg.ContainerId {
//				removeIdx = idx
//				break
//			}
//		}
//		if removeIdx >= 0 {
//			var keep []*Container
//			for i, cont := range containers {
//				if i == removeIdx {
//					log.Info("handler: OnContainerExited - restarting component", "component", cont.component.Name)
//					newContainer, newContainerId, err := f.restartComponent(cont, true,
//						f.reqChanByComponentLocked(cont.component.Name))
//					if err == nil {
//						keep = append(keep, newContainer)
//					} else {
//						log.Error("handler: OnContainerExited - unable to restart component", "err", err,
//							"component", cont.component.Name)
//						f.stopContainerQuietly(newContainerId, cont.component)
//					}
//				} else {
//					keep = append(keep, cont)
//				}
//			}
//			f.byComponentName[componentName] = keep
//			return
//		}
//	}
//}
//
//func (f *DockerHandlerFactory) OnImageUpdated(msg common.ImageUpdatedMessage) {
//	f.lock.Lock()
//	defer f.lock.Unlock()
//
//	for componentName, containers := range f.byComponentName {
//		var keep []*Container
//		for _, cont := range containers {
//			if normalizeImageName(cont.component.Docker.Image) == msg.ImageName {
//				log.Info("handler: OnImageUpdated - stopping image for component", "component", componentName,
//					"imageName", msg.ImageName, "newImageId", msg.ImageId)
//				newContainer, newContainerId, err := f.restartComponent(cont, false,
//					f.reqChanByComponentLocked(componentName))
//				if err == nil {
//					keep = append(keep, newContainer)
//				} else {
//					log.Error("handler: OnImageUpdated - unable to restart component", "err", err,
//						"component", componentName)
//					f.stopContainerQuietly(newContainerId, cont.component)
//				}
//			} else {
//				keep = append(keep, cont)
//			}
//		}
//		f.byComponentName[componentName] = keep
//	}
//}
//
//func (f *DockerHandlerFactory) OnComponentNotification(cn v1.DataChangedUnion) {
//	f.lock.Lock()
//	defer f.lock.Unlock()
//
//	if cn.PutComponent != nil {
//		containerRestarted := false
//		containers, ok := f.byComponentName[cn.PutComponent.Name]
//		if ok {
//			var keep []*Container
//			for _, cont := range containers {
//				if cont.component.Version < cn.PutComponent.Version {
//					log.Info("handler: OnComponentNotification restarting container",
//						"component", cn.PutComponent.Name, "containerId", cont.containerId[0:8])
//					newContainer, newContainerId, err := f.restartComponent(cont, false,
//						f.reqChanByComponentLocked(cn.PutComponent.Name))
//					if err == nil {
//						containerRestarted = true
//						keep = append(keep, newContainer)
//					} else {
//						log.Error("handler: OnComponentNotification - unable to restart component", "err", err,
//							"component", cn.PutComponent.Name)
//						f.stopContainerQuietly(newContainerId, cont.component)
//					}
//				} else {
//					keep = append(keep, cont)
//				}
//			}
//			f.byComponentName[cn.PutComponent.Name] = keep
//		}
//		if !containerRestarted {
//			go f.tryPullImage(cn.PutComponent.Name)
//		}
//	} else if cn.RemoveComponent != nil {
//		f.stopContainersByComponent(cn.RemoveComponent.Name, "component removed")
//	}
//}
//
//func (f *DockerHandlerFactory) tryPullImage(componentName string) {
//	comp, err := f.db.GetComponent(componentName)
//	if err == nil {
//		if comp.Docker.PullImageOnPut {
//			err = pullImage(f.dockerClient, comp)
//			if err != nil {
//				log.Error("handler: OnComponentNotification - unable to pull component", "err", err,
//					"component", componentName)
//			}
//		}
//	} else {
//		log.Error("handler: OnComponentNotification - unable to load component", "err", err, "component", componentName)
//	}
//}
//
//func (f *DockerHandlerFactory) stopContainersByComponent(componentName string, reason string) {
//	f.lock.Lock()
//	containers := f.byComponentName[componentName]
//	f.byComponentName[componentName] = []*Container{}
//	f.lock.Unlock()
//	for _, cont := range containers {
//		cont.Stop(reason)
//	}
//}
//
//func (f *DockerHandlerFactory) Shutdown() {
//	allContainers := make([]*Container, 0)
//	reqChanByCompCopy := make(map[string]chan *MaelRequest)
//	activeCompNames := make(map[string]bool)
//	f.lock.Lock()
//	f.version++
//	for componentName, containers := range f.byComponentName {
//		if len(containers) > 0 {
//			activeCompNames[componentName] = true
//			allContainers = append(allContainers, containers...)
//			f.byComponentName[componentName] = []*Container{}
//		}
//	}
//	for comp, ch := range f.reqChanByComponent {
//		reqChanByCompCopy[comp] = ch
//	}
//	f.reqChanByComponent = make(map[string]chan *MaelRequest)
//	f.lock.Unlock()
//
//	// drain off all queued requests
//	wg := &sync.WaitGroup{}
//	for compName, _ := range activeCompNames {
//		reqCh, ok := reqChanByCompCopy[compName]
//		if ok {
//			// send one last request into channel. this will go to the end of the request
//			// queue. when the request completes we'll know the channel has been consumed
//			f.sendHealthCheckAsync(compName, reqCh, wg)
//		} else {
//			log.Warn("handler: shutdown req channel not found", "component", compName)
//		}
//	}
//	// wait for clients to finish - at this point all active components should be drained
//	wg.Wait()
//
//	// stop all containers
//	for _, cont := range allContainers {
//		cont.Stop("shutting down")
//	}
//}
//
//func (f *DockerHandlerFactory) sendHealthCheckAsync(compName string, reqCh chan *MaelRequest, wg *sync.WaitGroup) {
//	comp, err := f.db.GetComponent(compName)
//	if err != nil {
//		log.Error("handler: shutdown GetComponent error", "component", compName, "err", err)
//	} else {
//		healthCheckPath := "/"
//		if comp.Docker != nil && comp.Docker.HttpHealthCheckPath != "" {
//			healthCheckPath = comp.Docker.HttpHealthCheckPath
//		}
//		rw := httptest.NewRecorder()
//		url := fmt.Sprintf("http://127.0.0.1%s", healthCheckPath)
//		req, err := http.NewRequest("GET", url, nil)
//		if err != nil {
//			log.Error("handler: shutdown NewRequest error", "component", compName, "url", url, "err", err)
//		} else {
//			req.Header.Set("Maelstrom-Component", compName)
//			mr := &MaelRequest{
//				complete:  make(chan bool, 1),
//				startTime: time.Now(),
//				rw:        rw,
//				req:       req,
//			}
//			wg.Add(1)
//			go func() {
//				defer wg.Done()
//				log.Info("handler: shutdown waiting to drain component", "component", compName)
//				deadline := componentReqDeadline(0, comp)
//				deadlineCtx, _ := context.WithDeadline(context.Background(), deadline)
//				reqCh <- mr
//				select {
//				case <-deadlineCtx.Done():
//					log.Warn("handler: shutdown timeout draining component", "component", compName)
//					break
//				case <-mr.complete:
//					log.Info("handler: shutdown component drained successfully", "component", compName)
//					break
//				}
//			}()
//		}
//	}
//}
//
//func (f *DockerHandlerFactory) restartComponent(oldContainer *Container, stopAsync bool,
//	reqCh chan *MaelRequest) (*Container, string, error) {
//	component, err := f.db.GetComponent(oldContainer.component.Name)
//	if err != nil {
//		return nil, "", fmt.Errorf("restartComponent: error loading component: %s - %v", oldContainer.component.Name, err)
//	}
//
//	if stopAsync {
//		go oldContainer.Stop("restarting component")
//	} else {
//		oldContainer.Stop("restarting component")
//	}
//	return f.startContainer(component, reqCh)
//}
