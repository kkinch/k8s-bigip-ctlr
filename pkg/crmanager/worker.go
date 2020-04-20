/*-
* Copyright (c) 2016-2019, F5 Networks, Inc.
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
*
*    http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package crmanager

import (
	"fmt"
	"time"

	cisapiv1 "github.com/F5Networks/k8s-bigip-ctlr/config/apis/cis/v1"
	log "github.com/F5Networks/k8s-bigip-ctlr/pkg/vlogger"
	v1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

func (crMgr *CRManager) customResourceWorker() {
	log.Debugf("Starting Custom Resource Worker")
	for crMgr.processResource() {
	}
}

// processResource gets resources from the rscQueue and processes the resource depending  on its kind.
func (crMgr *CRManager) processResource() bool {

	key, quit := crMgr.rscQueue.Get()
	if quit {
		// The controller is shutting down.
		log.Debugf("Resource Queue is empty, Going to StandBy Mode")
		return false
	}
	defer crMgr.rscQueue.Done(key)
	rKey := key.(*rqKey)
	log.Debugf("Processing Key: %v", rKey)

	// Check the type of resource and process accordingly.
	switch rKey.kind {
	case VirtualServer:
		vs := rKey.rsc.(*cisapiv1.VirtualServer)
		err := crMgr.syncVirtualServer(vs)
		if err != nil {
			// TODO
			utilruntime.HandleError(fmt.Errorf("Sync %v failed with %v", key, err))
		}
		crMgr.Agent.PostConfig(crMgr.resources.GetAllResources())
		crMgr.rscQueue.Forget(key)
		return true
	case Service:
		svc := rKey.rsc.(*v1.Service)
		virtuals := crMgr.syncService(svc)
		// No Virtuals are effected with the change in service.
		if nil == virtuals {
			break
		}
		for _, virtual := range virtuals {
			err := crMgr.syncVirtualServer(virtual)
			if err != nil {
				// TODO
				utilruntime.HandleError(fmt.Errorf("Sync %v failed with %v", key, err))
			}
		}
		crMgr.rscQueue.Forget(key)
		return true
	case Endpoints:
		ep := rKey.rsc.(*v1.Endpoints)
		svc := crMgr.syncEndpoints(ep)
		// No Services are effected with the change in service.
		if nil == svc {
			break
		}
		virtuals := crMgr.syncService(svc)
		for _, virtual := range virtuals {
			err := crMgr.syncVirtualServer(virtual)
			if err != nil {
				// TODO
				utilruntime.HandleError(fmt.Errorf("Sync %v failed with %v", key, err))
			}
		}
		crMgr.rscQueue.Forget(key)
		return true
	default:
		log.Errorf("Unknown resource Kind: %v", rKey.kind)
	}

	crMgr.rscQueue.AddRateLimited(key)

	return true
}

// syncEndpoints gets the service associated with endpoints.
func (crMgr *CRManager) syncEndpoints(ep *v1.Endpoints) *v1.Service {

	epName := ep.ObjectMeta.Name
	epNamespace := ep.ObjectMeta.Namespace
	svcKey := fmt.Sprintf("%s/%s", epNamespace, epName)

	// Create namespaced Informer
	svc, exists, err := crMgr.crInformers[epNamespace].
		svcInformer.GetIndexer().GetByKey(svcKey)
	if err != nil {
		log.Infof("Error fetching service %v from the store: %v", svcKey, err)
		return nil
	}
	if !exists {
		log.Infof("Service %v doesn't exist", svcKey)
		return nil
	}

	return svc.(*v1.Service)
}

// syncService gets the List of VirtualServers which are effected by the addition/deletion/updation
// of service.
func (crMgr *CRManager) syncService(svc *v1.Service) []*cisapiv1.VirtualServer {

	allVirtuals := crMgr.getAllVirtualServers(svc.ObjectMeta.Namespace)
	if nil == allVirtuals {
		log.Infof("No VirtualServers founds in namespace %s",
			svc.ObjectMeta.Namespace)
		return nil
	}

	// find VirtualServers that reference the service
	virtualsForService := getVirtualServersForService(allVirtuals, svc)
	if nil == virtualsForService {
		log.Infof("Change in Service %s does not effect any VirtualServer",
			svc.ObjectMeta.Name)
		return nil
	}
	// Output list of all Virtuals Found.
	var targetVirtualNames []string
	for _, vs := range allVirtuals {
		targetVirtualNames = append(targetVirtualNames, vs.ObjectMeta.Name)
	}
	log.Debugf("VirtualServers %v are affected with service %s change",
		targetVirtualNames, svc.ObjectMeta.Name)

	// TODO
	// Remove Duplicate entries in the slice targetVirutalServers.
	// or Add only Unique entries into the slice targetVirutalServers.
	return virtualsForService
}

// getAllVirtualServers returns list of all valid VirtualServers in rkey namespace.
func (crMgr *CRManager) getAllVirtualServers(namespace string) []*cisapiv1.VirtualServer {
	var allVirtuals []*cisapiv1.VirtualServer

	// Get list of VirtualServers and process them.
	for _, obj := range crMgr.crInformers[namespace].
		vsInformer.GetIndexer().List() {

		vs := obj.(*cisapiv1.VirtualServer)
		// TODO
		// Validate the VirtualServers List to check if all the vs are valid.

		allVirtuals = append(allVirtuals, vs)
	}

	return allVirtuals
}

func getVirtualServersForService(allVirtuals []*cisapiv1.VirtualServer,
	svc *v1.Service) []*cisapiv1.VirtualServer {

	var result []*cisapiv1.VirtualServer
	svcName := svc.ObjectMeta.Name
	svcNamespace := svc.ObjectMeta.Namespace

	for _, vs := range allVirtuals {
		if vs.ObjectMeta.Namespace != svcNamespace {
			continue
		}

		isValidVirtual := false
		for _, pool := range vs.Spec.Pools {
			if pool.Service == svcName {
				isValidVirtual = true
				break
			}
		}
		if !isValidVirtual {
			continue
		}

		result = append(result, vs)
	}

	return result
}

func (crMgr *CRManager) syncVirtualServer(virtual *cisapiv1.VirtualServer) error {

	startTime := time.Now()
	defer func() {
		endTime := time.Now()
		log.Debugf("Finished syncing virtual servers %+v (%v)",
			virtual, endTime.Sub(startTime))
	}()
	// check if the virutal server matches all the requirements.
	vkey := virtual.ObjectMeta.Namespace + "/" + virtual.ObjectMeta.Name
	valid := crMgr.checkValidVirtualServer(virtual)
	if false == valid {
		log.Infof("Ignoring VirtualServer %s, invalid configuration or deleted",
			vkey)

		return nil
	}

	// Get a list of dependencies removed so their pools can be removed.
	objKey, objDeps := NewObjectDependencies(virtual)

	virtualLookupFunc := func(key ObjectDependency) bool {
		return false
	}

	// TODO ==> UpdateDependencies to get the added and removed deps.
	_, depsRemoved := crMgr.resources.UpdateDependencies(
		objKey, objDeps, virtualLookupFunc)

	// Depending on the ports defined, TLS type or Unsecured we will populate the resource config.
	portStructs := crMgr.virtualPorts(virtual)
	for _, portStruct := range portStructs {
		rsCfg := crMgr.createRSConfigFromVirtualServer(
			virtual,
			virtual.ObjectMeta.Namespace,
			portStruct,
		)
		if rsCfg == nil {
			// Currently, an error is returned only if the VirtualServer is one we
			// do not care about
			continue
		}

		// Collect all service names on this VirtualServer.
		// Used in handleConfigForType.
		var svcs []string
		for _, pl := range virtual.Spec.Pools {
			svcs = append(svcs, pl.Service)

		}

		log.Debugf("resourceconfig looks like %v", rsCfg)

		// Remove any dependencies no longer used by this VirtualServer
		for _, dep := range depsRemoved {
			if dep.Kind == RuleDep {
				for _, pol := range rsCfg.Policies {
					for _, rl := range pol.Rules {
						if rl.FullURI == dep.Name {
							rsCfg.DeleteRuleFromPolicy(pol.Name, rl, crMgr.mergedRulesMap)
						}
					}
				}
			}
		}

		if crMgr.ControllerMode == "nodeport" {
			_, _, _ =
				crMgr.updatePoolMembersForNodePort(rsCfg)
		} else {
			_, _, _ =
				crMgr.updatePoolMembersForCluster(rsCfg)
		}

		/** TODO ==> To be implemented in ALPHA later stage
		if ok, found, updated := crMgr.handleConfigForType(
			rsCfg, rsMap, rsName,
			crInf, virtual); !ok {
			stats.vsUpdated += updated
			continue
		} else {
			if updated > 0 && !appMgr.processAllMultiSvc(len(rsCfg.Pools),
				rsCfg.GetName()) {
				updated -= 1
			}
			stats.vsFound += found
			stats.vsUpdated += updated
			if updated > 0 {
				msg := fmt.Sprintf(
					"Created a ResourceConfig '%v' for the Ingress.",
					rsCfg.GetName())
				appMgr.recordIngressEvent(ing, "ResourceConfigured", msg)
			}
		}
		// Set the Ingress Status IP address
		appMgr.setIngressStatus(ing, rsCfg)
		**/

	}

	/** TODO ==> To be implemented in ALPHA later stage
	// rsMap stores all resources currently in Resources matching sKey, indexed by port.
	// At the end of processing, rsMap should only contain configs we want to delete.
	// If we have a valid config, then we remove it from rsMap.
	rsMap := appMgr.getResourcesForKey(sKey)
	dgMap := make(InternalDataGroupMap)

	var stats vsSyncStats
	appMgr.rsrcSSLCtxt = make(map[string]*v1.Secret)
	// Update internal data groups if changed
	appMgr.syncDataGroups(&stats, dgMap, sKey.Namespace)
	// Delete IRules if necessary
	appMgr.syncIRules()


	if len(rsMap) > 0 {
		// We get here when there are ports defined in the service that don't
		// have a corresponding config map.
		stats.vsDeleted += appMgr.deleteUnusedConfigs(sKey, rsMap)
		stats.vsUpdated += appMgr.deleteUnusedResources(sKey, svcFound)

	} else if !svcFound {
		stats.vsUpdated += appMgr.deleteUnusedResources(sKey, svcFound)
	}

	log.Debugf("Updated %v of %v virtual server configs, deleted %v",
		stats.vsUpdated, stats.vsFound, stats.vsDeleted)

	// delete any custom profiles that are no longer referenced
	appMgr.deleteUnusedProfiles(appInf, sKey.Namespace, &stats)

	switch {
	case stats.vsUpdated > 0,
		stats.vsDeleted > 0,
		stats.cpUpdated > 0,
		stats.dgUpdated > 0,
		stats.poolsUpdated > 0,
		!appMgr.steadyState && appMgr.processedItems >= appMgr.queueLen:
		{
			appMgr.outputConfig()
		}
	}
	**/

	return nil
}

func (crMgr *CRManager) updatePoolMembersForNodePort(
	rsCfg *ResourceConfig,
) (bool, string, string) {
	// TODO: Can we get rid of counter?
	index := 0
	for _, pool := range rsCfg.Pools {
		svcName := pool.ServiceName
		svcKey := "default/" + svcName
		// TODO: Too Many API calls?
		// TODO: Get ServiceName, Do not use default.
		service, _, _ := crMgr.crInformers["default"].
			svcInformer.GetIndexer().GetByKey(svcKey)
		svc := service.(*v1.Service)
		// Traverse for all the pools in the Resource Config
		if svc.Spec.Type == v1.ServiceTypeNodePort ||
			svc.Spec.Type == v1.ServiceTypeLoadBalancer {
			for _, portSpec := range svc.Spec.Ports {
				rsCfg.MetaData.Active = true
				rsCfg.Pools[index].Members =
					crMgr.getEndpointsForNodePort(portSpec.NodePort)

			}
		} else {
			log.Debugf("Requested service backend %s not of NodePort or LoadBalancer type",
				svcName)
		}
	}
	index++

	return true, "", ""
}

func (crMgr *CRManager) updatePoolMembersForCluster(
	rsCfg *ResourceConfig,
) (bool, string, string) {

	index := 0
	for _, pool := range rsCfg.Pools {
		svcName := pool.ServiceName
		svcKey := "default/" + svcName
		// TODO: Too Many API calls?
		// TODO: Get ServiceName, Do not use default.
		item, found, _ := crMgr.crInformers["default"].
			epsInformer.GetStore().Get(svcKey)
		if !found {
			log.Debugf("Endpoints for service '%v' not found!", svcKey)
			return false, "", ""
		}
		eps, _ := item.(*v1.Endpoints)
		// Get service
		service, _, _ := crMgr.crInformers["default"].
			svcInformer.GetIndexer().GetByKey(svcKey)
		svc := service.(*v1.Service)

		for _, portSpec := range svc.Spec.Ports {
			ipPorts := crMgr.getEndpointsForCluster(portSpec.Name, eps)
			log.Debugf("Found endpoints for backend %+v: %v", svcKey, ipPorts)
			rsCfg.MetaData.Active = true
			rsCfg.Pools[index].Members = ipPorts
		}
	}
	index++

	return true, "", ""
}

func (crMgr *CRManager) getEndpointsForNodePort(
	nodePort int32,
) []Member {
	// Fake data, to be replaced by node polling implementation.
	var members []Member
	member := Member{
		Address: "100.100.100.100",
		Port:    80,
		Session: "user-enabled",
	}
	members = append(members, member)
	return members
}

func (crMgr *CRManager) getEndpointsForCluster(
	portName string,
	eps *v1.Endpoints,
) []Member {
	// Fake data, to be replaced by node polling implementation.
	var members []Member
	member := Member{
		Address: "100.100.100.100",
		Port:    80,
		Session: "user-enabled",
	}
	members = append(members, member)
	return members
}