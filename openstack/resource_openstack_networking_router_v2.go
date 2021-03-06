package openstack

import (
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/layer3/routers"
)

func resourceNetworkingRouterV2() *schema.Resource {
	return &schema.Resource{
		Create: resourceNetworkingRouterV2Create,
		Read:   resourceNetworkingRouterV2Read,
		Update: resourceNetworkingRouterV2Update,
		Delete: resourceNetworkingRouterV2Delete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},

		Schema: map[string]*schema.Schema{
			"region": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Computed: true,
			},
			"name": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: false,
			},
			"admin_state_up": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
				ForceNew: false,
				Computed: true,
			},
			"distributed": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
				ForceNew: true,
				Computed: true,
			},
			"external_gateway": &schema.Schema{
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      false,
				Computed:      true,
				Deprecated:    "use external_network_id instead",
				ConflictsWith: []string{"external_network_id"},
			},
			"external_network_id": &schema.Schema{
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      false,
				Computed:      true,
				ConflictsWith: []string{"external_gateway"},
			},
			"enable_snat": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
				ForceNew: false,
				Computed: true,
			},
			"external_fixed_ip": &schema.Schema{
				Type:     schema.TypeList,
				Optional: true,
				ForceNew: false,
				Computed: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"subnet_id": &schema.Schema{
							Type:     schema.TypeString,
							Optional: true,
						},
						"ip_address": &schema.Schema{
							Type:     schema.TypeString,
							Optional: true,
						},
					},
				},
			},
			"tenant_id": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Computed: true,
			},
			"value_specs": &schema.Schema{
				Type:     schema.TypeMap,
				Optional: true,
				ForceNew: true,
			},
			"availability_zone_hints": &schema.Schema{
				Type:     schema.TypeList,
				Computed: true,
				ForceNew: true,
				Optional: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
		},
	}
}

func resourceNetworkingRouterV2Create(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*Config)
	networkingClient, err := config.networkingV2Client(GetRegion(d, config))
	if err != nil {
		return fmt.Errorf("Error creating OpenStack networking client: %s", err)
	}

	createOpts := RouterCreateOpts{
		routers.CreateOpts{
			Name:                  d.Get("name").(string),
			TenantID:              d.Get("tenant_id").(string),
			AvailabilityZoneHints: resourceNetworkingAvailabilityZoneHintsV2(d),
		},
		MapValueSpecs(d),
	}

	if asuRaw, ok := d.GetOk("admin_state_up"); ok {
		asu := asuRaw.(bool)
		createOpts.AdminStateUp = &asu
	}

	if dRaw, ok := d.GetOk("distributed"); ok {
		d := dRaw.(bool)
		createOpts.Distributed = &d
	}

	// Gateway settings
	var externalNetworkID string
	if v := d.Get("external_gateway").(string); v != "" {
		externalNetworkID = v
	}

	if v := d.Get("external_network_id").(string); v != "" {
		externalNetworkID = v
	}

	if externalNetworkID != "" {
		gatewayInfo := routers.GatewayInfo{
			NetworkID: externalNetworkID,
		}
		createOpts.GatewayInfo = &gatewayInfo
	}

	if esRaw, ok := d.GetOk("enable_snat"); ok {
		if externalNetworkID == "" {
			return fmt.Errorf("setting enable_snat requires external_network_id to be set")
		}
		es := esRaw.(bool)
		createOpts.GatewayInfo.EnableSNAT = &es
	}

	externalFixedIPs := resourceRouterExternalFixedIPsV2(d)
	if len(externalFixedIPs) > 0 {
		if externalNetworkID == "" {
			return fmt.Errorf("setting an external_fixed_ip requires external_network_id to be set")
		}
		createOpts.GatewayInfo.ExternalFixedIPs = externalFixedIPs
	}

	log.Printf("[DEBUG] Create Options: %#v", createOpts)
	n, err := routers.Create(networkingClient, createOpts).Extract()
	if err != nil {
		return fmt.Errorf("Error creating OpenStack Neutron router: %s", err)
	}
	log.Printf("[INFO] Router ID: %s", n.ID)

	log.Printf("[DEBUG] Waiting for OpenStack Neutron Router (%s) to become available", n.ID)
	stateConf := &resource.StateChangeConf{
		Pending:    []string{"BUILD", "PENDING_CREATE", "PENDING_UPDATE"},
		Target:     []string{"ACTIVE"},
		Refresh:    waitForRouterActive(networkingClient, n.ID),
		Timeout:    d.Timeout(schema.TimeoutCreate),
		Delay:      5 * time.Second,
		MinTimeout: 3 * time.Second,
	}

	_, err = stateConf.WaitForState()

	d.SetId(n.ID)

	return resourceNetworkingRouterV2Read(d, meta)
}

func resourceNetworkingRouterV2Read(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*Config)
	networkingClient, err := config.networkingV2Client(GetRegion(d, config))
	if err != nil {
		return fmt.Errorf("Error creating OpenStack networking client: %s", err)
	}

	n, err := routers.Get(networkingClient, d.Id()).Extract()
	if err != nil {
		if _, ok := err.(gophercloud.ErrDefault404); ok {
			d.SetId("")
			return nil
		}

		return fmt.Errorf("Error retrieving OpenStack Neutron Router: %s", err)
	}

	log.Printf("[DEBUG] Retrieved Router %s: %+v", d.Id(), n)

	d.Set("name", n.Name)
	d.Set("admin_state_up", n.AdminStateUp)
	d.Set("distributed", n.Distributed)
	d.Set("tenant_id", n.TenantID)
	d.Set("region", GetRegion(d, config))

	if err := d.Set("availability_zone_hints", n.AvailabilityZoneHints); err != nil {
		log.Printf("[DEBUG] unable to set availability_zone_hints: %s", err)
	}

	// Gateway settings
	d.Set("external_gateway", n.GatewayInfo.NetworkID)
	d.Set("external_network_id", n.GatewayInfo.NetworkID)
	d.Set("enable_snat", n.GatewayInfo.EnableSNAT)

	var externalFixedIPs []map[string]string
	for _, v := range n.GatewayInfo.ExternalFixedIPs {
		externalFixedIPs = append(externalFixedIPs, map[string]string{
			"subnet_id":  v.SubnetID,
			"ip_address": v.IPAddress,
		})
	}

	if err = d.Set("external_fixed_ip", externalFixedIPs); err != nil {
		log.Printf("[DEBUG] unable to set external_fixed_ip: %s", err)
	}

	return nil
}

func resourceNetworkingRouterV2Update(d *schema.ResourceData, meta interface{}) error {
	routerId := d.Id()
	osMutexKV.Lock(routerId)
	defer osMutexKV.Unlock(routerId)

	config := meta.(*Config)
	networkingClient, err := config.networkingV2Client(GetRegion(d, config))
	if err != nil {
		return fmt.Errorf("Error creating OpenStack networking client: %s", err)
	}

	var updateOpts routers.UpdateOpts
	if d.HasChange("name") {
		updateOpts.Name = d.Get("name").(string)
	}
	if d.HasChange("admin_state_up") {
		asu := d.Get("admin_state_up").(bool)
		updateOpts.AdminStateUp = &asu
	}

	// Gateway settings
	var updateGatewaySettings bool
	var externalNetworkID string
	gatewayInfo := routers.GatewayInfo{}

	if v := d.Get("external_gateway").(string); v != "" {
		externalNetworkID = v
	}

	if v := d.Get("external_network_id").(string); v != "" {
		externalNetworkID = v
	}

	if externalNetworkID != "" {
		gatewayInfo.NetworkID = externalNetworkID
	}

	if d.HasChange("external_gateway") {
		updateGatewaySettings = true
	}

	if d.HasChange("external_network_id") {
		updateGatewaySettings = true
	}

	if d.HasChange("enable_snat") {
		updateGatewaySettings = true
		if externalNetworkID == "" {
			return fmt.Errorf("setting enable_snat requires external_network_id to be set")
		}

		enableSNAT := d.Get("enable_snat").(bool)
		gatewayInfo.EnableSNAT = &enableSNAT
	}

	if d.HasChange("external_fixed_ip") {
		updateGatewaySettings = true

		externalFixedIPs := resourceRouterExternalFixedIPsV2(d)
		gatewayInfo.ExternalFixedIPs = externalFixedIPs
		if len(externalFixedIPs) > 0 {
			if externalNetworkID == "" {
				return fmt.Errorf("setting an external_fixed_ip requires external_network_id to be set")
			}
		}
	}

	if updateGatewaySettings {
		updateOpts.GatewayInfo = &gatewayInfo
	}

	log.Printf("[DEBUG] Updating Router %s with options: %+v", d.Id(), updateOpts)

	_, err = routers.Update(networkingClient, d.Id(), updateOpts).Extract()
	if err != nil {
		return fmt.Errorf("Error updating OpenStack Neutron Router: %s", err)
	}

	return resourceNetworkingRouterV2Read(d, meta)
}

func resourceNetworkingRouterV2Delete(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*Config)
	networkingClient, err := config.networkingV2Client(GetRegion(d, config))
	if err != nil {
		return fmt.Errorf("Error creating OpenStack networking client: %s", err)
	}

	stateConf := &resource.StateChangeConf{
		Pending:    []string{"ACTIVE"},
		Target:     []string{"DELETED"},
		Refresh:    waitForRouterDelete(networkingClient, d.Id()),
		Timeout:    d.Timeout(schema.TimeoutDelete),
		Delay:      5 * time.Second,
		MinTimeout: 3 * time.Second,
	}

	_, err = stateConf.WaitForState()
	if err != nil {
		return fmt.Errorf("Error deleting OpenStack Neutron Router: %s", err)
	}

	d.SetId("")
	return nil
}

func waitForRouterActive(networkingClient *gophercloud.ServiceClient, routerId string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		r, err := routers.Get(networkingClient, routerId).Extract()
		if err != nil {
			return nil, r.Status, err
		}

		log.Printf("[DEBUG] OpenStack Neutron Router: %+v", r)
		return r, r.Status, nil
	}
}

func waitForRouterDelete(networkingClient *gophercloud.ServiceClient, routerId string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		log.Printf("[DEBUG] Attempting to delete OpenStack Router %s.\n", routerId)

		r, err := routers.Get(networkingClient, routerId).Extract()
		if err != nil {
			if _, ok := err.(gophercloud.ErrDefault404); ok {
				log.Printf("[DEBUG] Successfully deleted OpenStack Router %s", routerId)
				return r, "DELETED", nil
			}
			return r, "ACTIVE", err
		}

		err = routers.Delete(networkingClient, routerId).ExtractErr()
		if err != nil {
			if _, ok := err.(gophercloud.ErrDefault404); ok {
				log.Printf("[DEBUG] Successfully deleted OpenStack Router %s", routerId)
				return r, "DELETED", nil
			}
			return r, "ACTIVE", err
		}

		log.Printf("[DEBUG] OpenStack Router %s still active.\n", routerId)
		return r, "ACTIVE", nil
	}
}

func resourceRouterExternalFixedIPsV2(d *schema.ResourceData) []routers.ExternalFixedIP {
	var externalFixedIPs []routers.ExternalFixedIP
	eFIPs := d.Get("external_fixed_ip").([]interface{})

	for _, eFIP := range eFIPs {
		v := eFIP.(map[string]interface{})
		fip := routers.ExternalFixedIP{
			SubnetID:  v["subnet_id"].(string),
			IPAddress: v["ip_address"].(string),
		}
		externalFixedIPs = append(externalFixedIPs, fip)
	}

	return externalFixedIPs
}
