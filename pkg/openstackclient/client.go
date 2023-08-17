package openstackclient

import (
	"bytes"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/startstop"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/gophercloud/utils/openstack/clientconfig"
	"github.com/openshift/hive/pkg/constants"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
)

//go:generate mockgen -source=./client.go -destination=./mock/client_generated.go -package=mock

// Client is a wrapper object for Openstack clients to allow for easier testing.
type Client interface {
	// Compute
	StopServer(serverId string) startstop.StopResult
	StartServer(serverId string) startstop.StartResult
	ListServers(opts servers.ListOpts) pagination.Pager
}

type openstackClient struct {
	computeClient *gophercloud.ServiceClient
}

// yamlOptsBuilder lets us provide our own functions to return a 'clouds.yaml' file that has been
// unmarshaled into the format expected by the OpenStack clients.
type yamlOptsBuilder struct {
	cloudYaml map[string]clientconfig.Cloud
}

func (c *openstackClient) StopServer(serverId string) startstop.StopResult {
	return startstop.Stop(c.computeClient, serverId)
}

func (c *openstackClient) StartServer(serverId string) startstop.StartResult {
	return startstop.Start(c.computeClient, serverId)
}

func (c *openstackClient) ListServers(opts servers.ListOpts) pagination.Pager {
	return servers.List(c.computeClient, opts)
}

// NewClientWithCustomCertificate creates our client wrapper object for the openstack clients we use with CA certifiacates mentioned in the secret
func NewClientWithCustomCertificate(secret *corev1.Secret, cloudName string, buff bytes.Buffer) (Client, error) {

	yamlOpts, err := newYamlOptsBuilder(secret)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create yamlOpts for openstack client")
	}

	clientOptions := &clientconfig.ClientOpts{
		Cloud:    cloudName,
		YAMLOpts: yamlOpts,
	}

	if err := yamlOpts.updateTrust(clientOptions.Cloud, buff.Bytes()); err != nil {
		return nil, errors.Wrap(err, "failed to update trust in the yamlOpts")
	}
	clientOptions.YAMLOpts = yamlOpts

	compute, err := clientconfig.NewServiceClient("compute", clientOptions)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get a new compute client")
	}

	return &openstackClient{
		computeClient: compute,
	}, nil

}

// NewClient creates our client wrapper object for the openstack clients we use.
func NewClient(secret *corev1.Secret, cloudName string) (Client, error) {

	yamlOpts, err := newYamlOptsBuilder(secret)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create yamlOpts for openstack client")
	}

	clientOptions := &clientconfig.ClientOpts{
		Cloud:    cloudName,
		YAMLOpts: yamlOpts,
	}

	compute, err := clientconfig.NewServiceClient("compute", clientOptions)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get a new compute client")
	}

	return &openstackClient{
		computeClient: compute,
	}, nil

}

func newYamlOptsBuilder(secret *corev1.Secret) (*yamlOptsBuilder, error) {

	cloudsYaml, ok := secret.Data[constants.OpenStackCredentialsName]
	if !ok {
		return nil, errors.New("did not find credentials in the OpenStack credentials secret")
	}

	var clouds clientconfig.Clouds
	if err := yaml.Unmarshal(cloudsYaml, &clouds); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal yaml stored in secret")
	}

	optsBuilder := &yamlOptsBuilder{
		cloudYaml: clouds.Clouds,
	}
	return optsBuilder, nil
}

func (opts *yamlOptsBuilder) LoadCloudsYAML() (map[string]clientconfig.Cloud, error) {
	return opts.cloudYaml, nil
}

func (opts *yamlOptsBuilder) LoadSecureCloudsYAML() (map[string]clientconfig.Cloud, error) {
	// secure.yaml is optional so just pretend it doesn't exist
	return nil, nil
}

func (opts *yamlOptsBuilder) LoadPublicCloudsYAML() (map[string]clientconfig.Cloud, error) {
	return nil, errors.Errorf("LoadPublicCloudsYAML() not implemented")
}

func (opts *yamlOptsBuilder) updateTrust(cloud string, trust []byte) error {
	conf, ok := opts.cloudYaml[cloud]
	if !ok {
		return errors.Errorf("no cloud %s found", cloud)
	}
	conf.CACertFile = string(trust)
	opts.cloudYaml[cloud] = conf
	return nil
}
