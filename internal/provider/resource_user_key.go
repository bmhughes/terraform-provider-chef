package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	chefc "github.com/go-chef/chef"
)

func resourceChefUserKey() *schema.Resource {
	return &schema.Resource{
		CreateContext: CreateUserKey,
		UpdateContext: UpdateUserKey,
		ReadContext:   ReadUserKey,
		DeleteContext: DeleteUserKey,

		Schema: map[string]*schema.Schema{
			"user": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"key_name": {
				Type:     schema.TypeString,
				Optional: true,
				Default:  "default",
			},
			"public_key": {
				Type:     schema.TypeString,
				Required: true,
			},
		},
	}
}

type chefUserKey struct {
	User string
	Key  chefc.AccessKey
}

func CreateUserKey(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*chefClient)

	key, err := userKeyFromResourceData(d)
	if err != nil {
		return err
	}

	if _, err := c.Global.Users.AddKey(key.User, key.Key); err != nil {
		return diag.Diagnostics{
			{
				Severity:      diag.Error,
				Summary:       "Error creating user key",
				Detail:        fmt.Sprint(err),
				AttributePath: cty.GetAttrPath("key_name"),
			},
		}
	}

	d.SetId(key.User + "+" + key.Key.Name)
	return ReadUserKey(ctx, d, meta)
}

func UpdateUserKey(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*chefClient)

	key, err := userKeyFromResourceData(d)
	if err != nil {
		return err
	}

	if _, err := c.Global.Users.UpdateKey(key.User, key.Key.Name, key.Key); err != nil {
		return diag.Diagnostics{
			{
				Severity:      diag.Error,
				Summary:       "Error updating user key",
				Detail:        fmt.Sprint(err),
				AttributePath: cty.GetAttrPath("key_name"),
			},
		}
	}

	d.SetId(key.User + "+" + key.Key.Name)
	return ReadUserKey(ctx, d, meta)
}

func ReadUserKey(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*chefClient)

	key, err := userKeyFromResourceData(d)
	if err != nil {
		return err
	}

	if k, err := c.Global.Users.GetKey(key.User, key.Key.Name); err == nil {
		d.Set("user", key.User)
		d.Set("key_name", k.Name)
		d.Set("public_key", k.PublicKey)
	} else {
		if errRes, ok := err.(*chefc.ErrorResponse); ok {
			if errRes.Response.StatusCode == 404 {
				d.SetId("")
			}
		} else {
			return diag.Diagnostics{
				{
					Severity:      diag.Error,
					Summary:       "Error reading user key",
					Detail:        fmt.Sprint(err),
					AttributePath: cty.GetAttrPath("key_name"),
				},
			}
		}
	}
	return nil
}

func DeleteUserKey(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*chefClient)

	key, err := userKeyFromResourceData(d)
	if err != nil {
		return err
	}
	if _, err := c.Global.Users.DeleteKey(key.User, key.Key.Name); err == nil {
		d.SetId("")
		return nil
	} else {
		return diag.Diagnostics{
			{
				Severity:      diag.Error,
				Summary:       "Error deleting user key",
				Detail:        fmt.Sprint(err),
				AttributePath: cty.GetAttrPath("key_name"),
			},
		}
	}
}

func userKeyFromResourceData(d *schema.ResourceData) (*chefUserKey, diag.Diagnostics) {
	key := &chefUserKey{
		User: d.Get("user").(string),
		Key: chefc.AccessKey{
			Name:           d.Get("key_name").(string),
			PublicKey:      d.Get("public_key").(string),
			ExpirationDate: "infinity",
		},
	}
	return key, nil
}
