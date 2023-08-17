package hibernation

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/extendedstatus"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/pagination"
	hivev1 "github.com/openshift/hive/apis/hive/v1"
	hivev1openstack "github.com/openshift/hive/apis/hive/v1/openstack"
	"github.com/openshift/hive/pkg/client/clientset/versioned/scheme"
	"github.com/openshift/hive/pkg/openstackclient"
	mockopenstackclient "github.com/openshift/hive/pkg/openstackclient/mock"
	testcd "github.com/openshift/hive/pkg/test/clusterdeployment"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ServersList struct {
	Servers []ServerWithExtendedStatus `json:"servers"`
}

var (
	activeTaskStates = []string{"POWERING_ON", "POWERING_OFF"}
	serverStates     = []string{"ACTIVE", "SHUTOFF"}
)

func TestOpenstackCanHandle(t *testing.T) {
	cd := testcd.BasicBuilder().Options(func(cd *hivev1.ClusterDeployment) {
		cd.Spec.Platform.OpenStack = &hivev1openstack.Platform{}
	}).Build()
	actuator := openstackActuator{}
	assert.True(t, actuator.CanHandle(cd))

	cd = testcd.BasicBuilder().Build()
	assert.False(t, actuator.CanHandle(cd))
}

func TestOpenstackStopAndStartMachines(t *testing.T) {
	tests := []struct {
		name        string
		testFunc    string
		instances   map[string]int
		setupClient func(*testing.T, *mockopenstackclient.MockClient)
	}{
		{
			name:      "stop no running instances",
			testFunc:  "StopMachines",
			instances: map[string]int{"POWERING_OFF": 2, "SHUTOFF": 1},
		},
		{
			name:      "stop running instances",
			testFunc:  "StopMachines",
			instances: map[string]int{"SHUTOFF": 2, "ACTIVE": 2},
			setupClient: func(t *testing.T, c *mockopenstackclient.MockClient) {
				setupOpenstackStopServerCalls(c, map[string]int{"ACTIVE": 2})
			},
		},
		{
			name:      "stop starting and running instances",
			testFunc:  "StopMachines",
			instances: map[string]int{"SHUTOFF": 3, "POWERING_OFF": 4, "POWERING_ON": 1, "ACTIVE": 3},
			setupClient: func(t *testing.T, c *mockopenstackclient.MockClient) {
				setupOpenstackStopServerCalls(c, map[string]int{"POWERING_ON": 1, "ACTIVE": 3})
			},
		},
		{
			name:      "start no stopped instances",
			testFunc:  "StartMachine",
			instances: map[string]int{"POWERING_ON": 4, "ACTIVE": 3},
		},
		{
			name:      "start stopped instances",
			testFunc:  "StartMachine",
			instances: map[string]int{"SHUTOFF": 3, "ACTIVE": 3},
			setupClient: func(t *testing.T, c *mockopenstackclient.MockClient) {
				setupOpenstackStartServerCalls(c, map[string]int{"SHUTOFF": 3})
			},
		},
		{
			name:      "start stopped and stopping instances",
			testFunc:  "StartMachine",
			instances: map[string]int{"SHUTOFF": 3, "POWERING_OFF": 1, "POWERING_ON": 3},
			setupClient: func(t *testing.T, c *mockopenstackclient.MockClient) {
				setupOpenstackStartServerCalls(c, map[string]int{"SHUTOFF": 3, "POWERING_OFF": 1})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			openstackClient := mockopenstackclient.NewMockClient(ctrl)
			setupOpenstackClientInstances(openstackClient, test.instances)
			if test.setupClient != nil {
				test.setupClient(t, openstackClient)
			}
			actuator := testOpenstackActuator(openstackClient)
			var err error
			switch test.testFunc {
			case "StopMachines":
				err = actuator.StopMachines(testOpenstackClusterDeployment(), nil, log.New())
			case "StartMachines":
				err = actuator.StartMachines(testOpenstackClusterDeployment(), nil, log.New())
			default:
				t.Fatal("Invalid function to test")
			}
			assert.NoError(t, err)
		})
	}
}

func TestOpenstackMachinesStoppedAndMachinesRunning(t *testing.T) {
	tests := []struct {
		name        string
		testFunc    string
		instances   map[string]int
		setupClient func(*testing.T, *mockopenstackclient.MockClient)
	}{
		{
			name:      "stop no running instances",
			testFunc:  "StopMachines",
			instances: map[string]int{"POWERING_OFF": 2, "SHUTOFF": 1},
		},
		{
			name:      "stop running instances",
			testFunc:  "StopMachines",
			instances: map[string]int{"POWERING_OFF": 5, "ACTIVE": 2},
			setupClient: func(t *testing.T, c *mockopenstackclient.MockClient) {
				setupOpenstackStopServerCalls(c, map[string]int{"ACTIVE": 2})
			},
		},
		{
			name:      "stop starting and running instances",
			testFunc:  "StopMachines",
			instances: map[string]int{"SHUTOFF": 3, "POWERING_OFF": 4, "POWERING_ON": 1, "ACTIVE": 3},
			setupClient: func(t *testing.T, c *mockopenstackclient.MockClient) {
				setupOpenstackStopServerCalls(c, map[string]int{"POWERING_ON": 1, "ACTIVE": 3})
			},
		},
		{
			name:      "start no stopped instances",
			testFunc:  "StartMachine",
			instances: map[string]int{"POWERING_ON": 4, "ACTIVE": 3},
		},
		{
			name:      "start stopped instances",
			testFunc:  "StartMachine",
			instances: map[string]int{"SHUTOFF": 3, "ACTIVE": 3},
			setupClient: func(t *testing.T, c *mockopenstackclient.MockClient) {
				setupOpenstackStartServerCalls(c, map[string]int{"SHUTOFF": 3})
			},
		},
		{
			name:      "start stopped and stopping instances",
			testFunc:  "StartMachine",
			instances: map[string]int{"SHUTOFF": 3, "POWERING_OFF": 1, "POWERING_ON": 3},
			setupClient: func(t *testing.T, c *mockopenstackclient.MockClient) {
				setupOpenstackStartServerCalls(c, map[string]int{"SHUTOFF": 3, "POWERING_OFF": 1})
			},
		},
	}
	fmt.Println(tests)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			openstackClient := mockopenstackclient.NewMockClient(ctrl)
			setupOpenstackClientInstances(openstackClient, test.instances)
			if test.setupClient != nil {
				test.setupClient(t, openstackClient)
			}
			actuator := testOpenstackActuator(openstackClient)
			var err error
			switch test.testFunc {
			case "StopMachines":
				err = actuator.StopMachines(testOpenstackClusterDeployment(), nil, log.New())
			case "StartMachines":
				err = actuator.StartMachines(testOpenstackClusterDeployment(), nil, log.New())
			default:
				t.Fatal("Invalid function to test")
			}
			assert.NoError(t, err)
		})
	}
}

func testOpenstackClusterDeployment() *hivev1.ClusterDeployment {
	cdBuilder := testcd.FullBuilder("testns", "testopenstackcluster", scheme.Scheme)
	return cdBuilder.Build(
		testcd.WithOpenstackPlatform(&hivev1openstack.Platform{Cloud: "openstack"}),
		testcd.WithClusterMetadata(&hivev1.ClusterMetadata{InfraID: "testopenstackcluster-foobarbaz"}),
	)
}

func testOpenstackActuator(openstackClient openstackclient.Client) *openstackActuator {
	return &openstackActuator{
		openstackClientFn: func(*hivev1.ClusterDeployment, client.Client, log.FieldLogger) (openstackclient.Client, error) {
			return openstackClient, nil
		},
	}
}

func setupOpenstackClientInstances(openstackClient *mockopenstackclient.MockClient, instances map[string]int) {

	srvrs := []ServerWithExtendedStatus{}
	for instance, count := range instances {
		for i := 0; i < count; i++ {
			name := fmt.Sprintf("%s-%d", instance, i)
			if slices.Contains(activeTaskStates, instance) {
				srvrs = append(srvrs, ServerWithExtendedStatus{
					Server: servers.Server{
						Name: name,
					},
					ServerExtendedStatusExt: extendedstatus.ServerExtendedStatusExt{
						TaskState: instance,
					},
				})
			} else {
				srvrs = append(srvrs, ServerWithExtendedStatus{
					Server: servers.Server{
						Name:   name,
						HostID: "",
						Status: instance,
					},
					ServerExtendedStatusExt: extendedstatus.ServerExtendedStatusExt{},
				})
			}
		}
	}

	serverslist := ServersList{
		Servers: srvrs,
	}
	srversBodybytes, err := json.Marshal(serverslist)
	if err != nil {
		fmt.Println(err.Error())
	}
	srversBody := string(srversBodybytes)

	fmt.Println(srversBody)

	pageresult := pagination.PageResult{
		Result: gophercloud.Result{Body: srversBody},
	}

	pager := pagination.NewPager(nil, "", func(r pagination.PageResult) pagination.Page {
		return servers.ServerPage{LinkedPageBase: pagination.LinkedPageBase{PageResult: pageresult}}
	})

	openstackClient.EXPECT().ListServers(gomock.Any()).Times(1).Return(pager)
}

func setupOpenstackStopServerCalls(client *mockopenstackclient.MockClient, instances map[string]int) {
	for state, count := range instances {
		for i := 0; i < count; i++ {
			client.EXPECT().StopServer(fmt.Sprintf("%s-%d", state, i)).Times(1)
		}
	}
}

func setupOpenstackStartServerCalls(client *mockopenstackclient.MockClient, servers map[string]int) {
	for state, count := range servers {
		for i := 0; i < count; i++ {
			client.EXPECT().StartServer(fmt.Sprintf("%s-%d", state, i)).Times(1)
		}
	}
}
