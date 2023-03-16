package openstack

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumetypes"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/networks"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/regions"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/users"
	"github.com/gophercloud/gophercloud/openstack/imageservice/v2/images"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/gophercloud/utils/openstack/clientconfig"
	liberr "github.com/konveyor/forklift-controller/pkg/lib/error"
	core "k8s.io/api/core/v1"
)

// Client struct
type Client struct {
	URL                 string
	Secret              *core.Secret
	provider            *gophercloud.ProviderClient
	identityService     *gophercloud.ServiceClient
	ComputeService      *gophercloud.ServiceClient
	ImageService        *gophercloud.ServiceClient
	BlockStorageService *gophercloud.ServiceClient
	log                 logr.Logger
}

// Connect.
func (r *Client) Connect() (err error) {
	var TLSClientConfig *tls.Config

	// if r.provider != nil {
	// 	return
	// }

	if r.insecureSkipVerify() {
		TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	} else {
		cacert := []byte(r.cacert())
		roots := x509.NewCertPool()
		ok := roots.AppendCertsFromPEM(cacert)
		if !ok {
			roots, err = x509.SystemCertPool()
			if err != nil {
				err = liberr.New("failed to configure the system's cert pool")
				return
			}
		}
		TLSClientConfig = &tls.Config{RootCAs: roots}
	}

	clientOpts := &clientconfig.ClientOpts{
		AuthInfo: &clientconfig.AuthInfo{
			AuthURL:     r.URL,
			Username:    r.username(),
			Password:    r.password(),
			ProjectName: r.projectName(),
			DomainName:  r.domainName(),
			AllowReauth: true,
		},
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 10 * time.Second,
				}).DialContext,
				MaxIdleConns:          10,
				IdleConnTimeout:       10 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				TLSClientConfig:       TLSClientConfig,
			},
		},
	}

	provider, err := clientconfig.AuthenticatedClient(clientOpts)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	r.provider = provider

	identityService, err := openstack.NewIdentityV3(r.provider, gophercloud.EndpointOpts{Region: r.region()})
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	r.identityService = identityService

	computeService, err := openstack.NewComputeV2(r.provider, gophercloud.EndpointOpts{Region: r.region()})
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	r.ComputeService = computeService

	imageService, err := openstack.NewImageServiceV2(r.provider, gophercloud.EndpointOpts{Region: r.region()})
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	r.ImageService = imageService

	blockStorageService, err := openstack.NewBlockStorageV3(r.provider, gophercloud.EndpointOpts{Region: r.region()})
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	r.BlockStorageService = blockStorageService

	return
}

// Username.
func (r *Client) username() string {
	if username, found := r.Secret.Data["username"]; found {
		return string(username)
	}
	return ""
}

// Password.
func (r *Client) password() string {
	if password, found := r.Secret.Data["password"]; found {
		return string(password)
	}
	return ""
}

// Project Name
func (r *Client) projectName() string {
	if projectName, found := r.Secret.Data["projectName"]; found {
		return string(projectName)
	}
	return ""
}

// Domain Name
func (r *Client) domainName() string {
	if domainName, found := r.Secret.Data["domainName"]; found {
		return string(domainName)
	}
	return ""
}

// Region
func (r *Client) region() string {
	if region, found := r.Secret.Data["regionName"]; found {
		return string(region)
	}
	return ""
}

// CA Certificate
func (r *Client) cacert() string {
	if cacert, found := r.Secret.Data["cacert"]; found {
		return string(cacert)
	}
	return ""
}

// insecureSkipVerify
func (r *Client) insecureSkipVerify() bool {
	if insecureSkipVerifyStr, found := r.Secret.Data["insecureSkipVerify"]; found {
		insecureSkipVerify, err := strconv.ParseBool(string(insecureSkipVerifyStr))
		if err != nil {
			return false
		}
		return insecureSkipVerify
	}
	return false
}

// List Servers.
func (r *Client) list(object interface{}, listopts interface{}) (err error) {

	var allPages pagination.Page

	switch object.(type) {
	case *[]Region:
		object := object.(*[]Region)
		allPages, err = regions.List(r.identityService, listopts.(*RegionListOpts)).AllPages()
		if err != nil {
			return
		}
		var regionList []regions.Region
		regionList, err = regions.ExtractRegions(allPages)
		if err != nil {
			return
		}
		var instanceList []Region
		for _, region := range regionList {
			// TODO implement support multiple regions/projects sync per user
			if region.ID == r.region() {
				instanceList = append(instanceList, Region{region})
			}
		}
		*object = instanceList
		return

	case *[]Project:
		object := object.(*[]Project)
		// TODO implement support multiple regions/projects sync per user
		opts := listopts.(*ProjectListOpts)
		opts.Name = r.projectName()
		allPages, err = projects.List(r.identityService, opts).AllPages()
		if err != nil {
			if !r.isForbidden(err) {
				return
			}
			*object, err = r.getUserProjects()
			return
		}
		var projectList []projects.Project
		projectList, err = projects.ExtractProjects(allPages)
		if err != nil {
			return
		}
		var instanceList []Project
		for _, project := range projectList {
			instanceList = append(instanceList, Project{project})
		}
		*object = instanceList
		return

	case *[]Flavor:
		object := object.(*[]Flavor)
		allPages, err = flavors.ListDetail(r.ComputeService, listopts.(*FlavorListOpts)).AllPages()
		if err != nil {
			return
		}
		var flavorList []flavors.Flavor
		flavorList, err = flavors.ExtractFlavors(allPages)
		if err != nil {
			return
		}
		var instanceList []Flavor
		var extraSpecs map[string]string
		for _, flavor := range flavorList {
			extraSpecs, err = flavors.ListExtraSpecs(r.ComputeService, flavor.ID).Extract()
			if err != nil {
				return
			}
			instanceList = append(instanceList, Flavor{Flavor: flavor, ExtraSpecs: extraSpecs})
		}
		*object = instanceList
		return

	case *[]Image:
		object := object.(*[]Image)
		allPages, err = images.List(r.ImageService, listopts.(*ImageListOpts)).AllPages()
		if err != nil {
			return
		}
		var imageList []images.Image
		imageList, err = images.ExtractImages(allPages)
		if err != nil {
			return
		}
		var instanceList []Image
		for _, image := range imageList {
			instanceList = append(instanceList, Image{image})
		}
		*object = instanceList
		return

	case *[]VM:
		object := object.(*[]VM)
		allPages, err = servers.List(r.ComputeService, listopts.(*VMListOpts)).AllPages()
		if err != nil {
			return
		}
		var serverList []servers.Server
		serverList, err = servers.ExtractServers(allPages)
		if err != nil {
			return
		}
		var instanceList []VM
		for _, server := range serverList {
			instanceList = append(instanceList, VM{server})
		}
		*object = instanceList
		return

	case *[]Snapshot:
		object := object.(*[]Snapshot)
		allPages, err = snapshots.List(r.BlockStorageService, nil).AllPages()
		if err != nil {
			return
		}
		var snapshotList []snapshots.Snapshot
		snapshotList, err = snapshots.ExtractSnapshots(allPages)
		if err != nil {
			return
		}
		var instanceList []Snapshot
		for _, snapshot := range snapshotList {
			instanceList = append(instanceList, Snapshot{snapshot})
		}
		*object = instanceList
		return

	case *[]Volume:
		object := object.(*[]Volume)
		allPages, err = volumes.List(r.BlockStorageService, listopts.(*VolumeListOpts)).AllPages()
		if err != nil {
			return
		}
		var volumeList []volumes.Volume
		volumeList, err = volumes.ExtractVolumes(allPages)
		if err != nil {
			return
		}
		var instanceList []Volume
		for _, volume := range volumeList {
			instanceList = append(instanceList, Volume{volume})
		}
		*object = instanceList
		return

	case *[]VolumeType:
		object := object.(*[]VolumeType)
		allPages, err = volumetypes.List(r.BlockStorageService, listopts.(*VolumeTypeListOpts)).AllPages()
		if err != nil {
			return
		}
		var volumeTypeList []volumetypes.VolumeType
		volumeTypeList, err = volumetypes.ExtractVolumeTypes(allPages)
		if err != nil {
			return
		}
		var instanceList []VolumeType
		for _, volumeType := range volumeTypeList {
			if volumeType.ExtraSpecs == nil {
				volumeType.ExtraSpecs = map[string]string{}
			}
			instanceList = append(instanceList, VolumeType{volumeType})
		}
		*object = instanceList
		return

	case *[]Network:
		object := object.(*[]Network)
		allPages, err = networks.List(r.ComputeService).AllPages()
		if err != nil {
			return
		}
		var networkList []networks.Network
		networkList, err = networks.ExtractNetworks(allPages)
		if err != nil {
			return
		}
		var instanceList []Network
		for _, network := range networkList {
			instanceList = append(instanceList, Network{network})
		}
		*object = instanceList
		return

	default:
		err = liberr.New(fmt.Sprintf("unsupported type %+v", object))
		return
	}
}

// Get a resource.
func (r *Client) get(object interface{}, ID string) (err error) {
	switch object.(type) {
	case *Region:
		var region *regions.Region
		region, err = regions.Get(r.identityService, ID).Extract()
		if err != nil {
			return
		}
		object = &Region{*region}
		return
	case *Project:
		var project *projects.Project
		project, err = projects.Get(r.identityService, ID).Extract()
		if err != nil {
			if !r.isForbidden(err) {
				return
			}
			object, err = r.getUserProject(ID)
			return
		}
		object = &Project{*project}
		return
	case *Flavor:
		var flavor *flavors.Flavor
		flavor, err = flavors.Get(r.ComputeService, ID).Extract()
		if err != nil {
			return
		}
		var extraSpecs map[string]string
		extraSpecs, err = flavors.ListExtraSpecs(r.ComputeService, ID).Extract()
		if err != nil {
			return
		}
		object = &Flavor{Flavor: *flavor, ExtraSpecs: extraSpecs}

		return
	case *Image:
		var image *images.Image
		image, err = images.Get(r.ImageService, ID).Extract()
		if err != nil {
			return
		}
		object = &Image{*image}
		return
	case *Snapshot:
		var snapshot *snapshots.Snapshot
		snapshot, err = snapshots.Get(r.BlockStorageService, ID).Extract()
		if err != nil {
			return
		}
		object = &Snapshot{*snapshot}
		return
	case *Volume:
		var volume *volumes.Volume
		volume, err = volumes.Get(r.BlockStorageService, ID).Extract()
		if err != nil {
			return
		}
		object = &Volume{*volume}
		return
	case *VolumeType:
		var volumeType *volumetypes.VolumeType
		volumeType, err = volumetypes.Get(r.BlockStorageService, ID).Extract()
		if err != nil {
			return
		}
		object = &VolumeType{*volumeType}
		return
	case *VM:
		var server *servers.Server
		server, err = servers.Get(r.ComputeService, ID).Extract()
		if err != nil {
			return
		}
		object = &VM{*server}
		return
	case *Network:
		var network *networks.Network
		network, err = networks.Get(r.ComputeService, ID).Extract()
		if err != nil {
			return
		}
		object = &Network{*network}
		return
	default:
		err = liberr.New(fmt.Sprintf("unsupported type %+v", object))
		return
	}
}

func (r *Client) isNotFound(err error) bool {
	switch liberr.Unwrap(err).(type) {
	case gophercloud.ErrResourceNotFound, gophercloud.ErrDefault404:
		return true
	default:
		return false
	}
}

func (r *Client) isForbidden(err error) bool {
	switch liberr.Unwrap(err).(type) {
	case gophercloud.ErrDefault403:
		return true
	default:
		return false
	}
}

func (r *Client) getAuthenticatedUserID() (string, error) {
	authResult := r.provider.GetAuthResult()
	if authResult == nil {
		//ProviderClient did not use openstack.Authenticate(), e.g. because token
		//was set manually with ProviderClient.SetToken()
		return "", liberr.New("no AuthResult available")
	}
	switch a := authResult.(type) {
	case tokens.CreateResult:
		u, err := a.ExtractUser()
		if err != nil {
			return "", err
		}
		return u.ID, nil
	default:
		return "", liberr.New(fmt.Sprintf("got unexpected AuthResult type: %T", a))

	}
}

func (r *Client) getUserProject(projectID string) (project *Project, err error) {
	var userProjects []Project
	var found bool
	userProjects, err = r.getUserProjects()
	if err != nil {
		return
	}
	for _, p := range userProjects {
		if p.ID == projectID {
			found = true
			project = &p
			break
		}
	}
	if !found {
		err = gophercloud.ErrDefault404{}
		return
	}
	return
}

func (r *Client) getUserProjects() (userProjects []Project, err error) {
	var userID string
	var allPages pagination.Page
	userID, err = r.getAuthenticatedUserID()
	if err != nil {
		return
	}
	allPages, err = users.ListProjects(r.identityService, userID).AllPages()
	if err != nil {
		return
	}
	var projectList []projects.Project
	projectList, err = projects.ExtractProjects(allPages)
	if err != nil {
		return
	}
	for _, project := range projectList {
		// TODO implement support multiple regions/projects sync per user
		if project.Name == r.projectName() {
			userProjects = append(userProjects, Project{project})
		}
	}
	return
}
