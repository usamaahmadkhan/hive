package hibernation

import (
	"bytes"
	"context"

	"github.com/gardener/controller-manager-library/pkg/logger"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/extendedstatus"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	hivev1 "github.com/openshift/hive/apis/hive/v1"
	controllerutils "github.com/openshift/hive/pkg/controller/utils"
	"github.com/openshift/hive/pkg/openstackclient"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ServerWithExtendedStatus struct {
	servers.Server
	extendedstatus.ServerExtendedStatusExt
}

var (
	// Uses a mix of server.Status and server.TaskState to determine the actual state of the server. See: https://wiki.openstack.org/wiki/VMState
	openstackRunningStates         = sets.NewString("ACTIVE")
	openstackShutdownStates        = sets.NewString("SHUTOFF")
	openstackTaskStateNone         = sets.NewString("")
	openstackTaskStateShuttingDown = sets.NewString("POWERING_OFF")
	openstackTaskStatePoweringOn   = sets.NewString("POWERING_ON")

	openstackStartedState = openstackRunningStates.Union(openstackTaskStateNone)  // Status: ACTIVE, TaskState NONE
	openstackStoppedState = openstackShutdownStates.Union(openstackTaskStateNone) // Status: SHUTOFF, TaskState NONE

	openstackStoppingOrStopped = openstackStoppedState.Union(openstackTaskStateShuttingDown) // Status: SHUTOFF, TaskState NONE | POWERING_OFF
	openstackStartingOrStarted = openstackStartedState.Union(openstackTaskStatePoweringOn)   // Status: ACTIVE, TaskState NONE | POWERING_ON

	openstackNotRunningStates = openstackStoppingOrStopped.Union(openstackTaskStatePoweringOn)   // Status: SHUTOFF, TaskState NONE | POWERING_OFF |  POWERING_ON
	openstackNotStoppedStates = openstackStartingOrStarted.Union(openstackTaskStateShuttingDown) // Status: ACTIVE, TaskState NONE | POWERING_OFF |  POWERING_ON
)

func init() {
	RegisterActuator(&openstackActuator{openstackClientFn: getOpenstackClient})
}

type openstackActuator struct {
	// openstackClientFn is the function to build an Openstack client, here for testing
	openstackClientFn func(*hivev1.ClusterDeployment, client.Client, log.FieldLogger) (openstackclient.Client, error)
}

// CanHandle returns true if the actuator can handle a particular ClusterDeployment
func (a *openstackActuator) CanHandle(cd *hivev1.ClusterDeployment) bool {
	return cd.Spec.Platform.OpenStack != nil
}

// StopMachines will stop machines belonging to the given ClusterDeployment
func (a *openstackActuator) StopMachines(cd *hivev1.ClusterDeployment, hiveClient client.Client, logger log.FieldLogger) error {
	logger = logger.WithField("cloud", "Openstack")
	openstackClient, err := a.openstackClientFn(cd, hiveClient, logger)
	if err != nil {
		return err
	}
	instances, err := getOpenstackClusterServers(cd, openstackClient, openstackStartingOrStarted, logger)
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		logger.Info("No instances were found to stop")
		return nil
	}
	var errs []error
	for _, instance := range instances {
		logger.WithField("instance", instance.Name).Info("Stopping instance")
		result := openstackClient.StopServer(instance.ID)
		if result.ExtractErr() != nil {
			errs = append(errs, result.ExtractErr())
			logger.WithError(err).WithField("instance", instance.Name).Error("Failed to stop instance")
		}
	}

	return utilerrors.NewAggregate(errs)
}

// StartMachines will start machines belonging to the given ClusterDeployment
func (a *openstackActuator) StartMachines(cd *hivev1.ClusterDeployment, hiveClient client.Client, logger log.FieldLogger) error {
	logger = logger.WithField("cloud", "Openstack")
	openstackClient, err := a.openstackClientFn(cd, hiveClient, logger)
	if err != nil {
		return err
	}
	instances, err := getOpenstackClusterServers(cd, openstackClient, openstackStoppingOrStopped, logger)
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		logger.Info("No instances were found to start")
		return nil
	}
	var errs []error
	for _, instance := range instances {
		logger.WithField("instance", instance.Name).Info("Starting instance")
		result := openstackClient.StartServer(instance.ID)
		if result.ExtractErr() != nil {
			errs = append(errs, result.ExtractErr())
			logger.WithError(err).WithField("instance", instance.Name).Error("Failed to start instance")
		}
	}

	return utilerrors.NewAggregate(errs)
}

// MachinesRunning will return true if machines belonging to the given ClusterDeployment are in running state
func (a *openstackActuator) MachinesRunning(cd *hivev1.ClusterDeployment, hiveClient client.Client, logger log.FieldLogger) (bool, []string, error) {
	logger = logger.WithField("cloud", "Openstack")
	logger.Infof("checking whether machines are running")
	openstackClient, err := a.openstackClientFn(cd, hiveClient, logger)
	if err != nil {
		return false, nil, err
	}
	instances, err := getOpenstackClusterServers(cd, openstackClient, openstackNotRunningStates, logger)
	if err != nil {
		return false, nil, err
	}
	return len(instances) == 0, getinstanceNames(instances), nil
}

// MachinesStopped will return true if machines belonging to the given ClusterDeployment are in stopped state
func (a *openstackActuator) MachinesStopped(cd *hivev1.ClusterDeployment, hiveClient client.Client, logger log.FieldLogger) (bool, []string, error) {
	logger = logger.WithField("cloud", "Openstack")
	logger.Infof("Checking whether machines are stopped")
	openstackClient, err := a.openstackClientFn(cd, hiveClient, logger)
	if err != nil {
		return false, nil, err
	}
	instances, err := getOpenstackClusterServers(cd, openstackClient, openstackNotStoppedStates, logger)
	if err != nil {
		return false, nil, err
	}
	return len(instances) == 0, getinstanceNames(instances), nil
}

func getOpenstackClient(cd *hivev1.ClusterDeployment, c client.Client, logger log.FieldLogger) (openstackclient.Client, error) {

	if cd.Spec.Platform.OpenStack == nil {
		return nil, errors.New("Openstack platform is not set in ClusterDeployment")
	}

	// One or more clouds can be specifed in the clouds.yaml. So only fetch client for the cloud specified.
	cloudName := cd.Spec.Platform.OpenStack.Cloud

	credentialsSecret := &corev1.Secret{}
	err := c.Get(context.TODO(), client.ObjectKey{Name: cd.Spec.Platform.OpenStack.CredentialsSecretRef.Name, Namespace: cd.Namespace}, credentialsSecret)
	if err != nil {
		logger.WithError(err).Log(controllerutils.LogLevel(err), "Failed to fetch Openstack credentials secret")
		return nil, errors.Wrap(err, "failed to fetch Openstack credentials secret")
	}

	var openstackClient openstackclient.Client
	// If CA certificate specified get a client with certficate
	if cd.Spec.Platform.OpenStack.CertificatesSecretRef != nil {
		customCASecretbuffer := &bytes.Buffer{}
		if err := controllerutils.TrustBundleFromSecretToWriter(c, credentialsSecret.Namespace, cd.Spec.Platform.OpenStack.CertificatesSecretRef.Name, customCASecretbuffer); err != nil {
			return nil, errors.Wrap(err, "failed to load trust bundle from CertificatesSecretRef")
		}
		openstackClient, err = openstackclient.NewClientWithCustomCertificate(credentialsSecret, cloudName, *customCASecretbuffer)
	} else {
		openstackClient, err = openstackclient.NewClient(credentialsSecret, cloudName)
	}

	if err != nil {
		logger.WithError(err).Error("failed to get Openstack client")
	}
	return openstackClient, err

}

func getinstanceNames(instances []ServerWithExtendedStatus) []string {
	result := make([]string, len(instances))
	for idx, i := range instances {
		result[idx] = i.Name
	}
	return result
}

func filterServersByStatus(allServers []ServerWithExtendedStatus, state sets.String) []ServerWithExtendedStatus {
	var result []ServerWithExtendedStatus
	for _, server := range allServers {
		logger.Debug(server)
		if state.Has(server.TaskState) && state.Has(server.Status) {
			result = append(result, server)
		}
	}
	return result
}

func getOpenstackClusterServers(cd *hivev1.ClusterDeployment, c openstackclient.Client, state sets.String, logger log.FieldLogger) ([]ServerWithExtendedStatus, error) {

	var allServers []ServerWithExtendedStatus

	clusterInfraPrefix := cd.Spec.ClusterMetadata.InfraID
	logger.Debug("listing cluster servers")

	opts := servers.ListOpts{
		Name: clusterInfraPrefix,
	}

	allPages, err := c.ListServers(opts).AllPages()
	if err != nil {
		logger.WithError(err).Error("failed to get a list of server pages for the cluster")
		return nil, err
	}

	err = servers.ExtractServersInto(allPages, &allServers)
	if err != nil {
		logger.WithError(err).Error("failed to extract extended status from servers")
	}

	result := filterServersByStatus(allServers, state)

	logger.WithField("count", len(result)).WithField("states", state.List()).Debug("result of listing servers")
	return result, nil
}
