package scim

import (
	"context"
	"fmt"
	"strings"

	"github.com/databricks/terraform-provider-databricks/common"
	"github.com/databricks/terraform-provider-databricks/workspace"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func userExistsErrorMessage(userName string, isAccount bool) string {
	if isAccount {
		return fmt.Sprintf("User with email %s already exists in this account", userName)
	} else {
		return fmt.Sprintf("User with username %s already exists.", userName)
	}
}

// ResourceUser manages users within workspace
func ResourceUser() *schema.Resource {
	type entity struct {
		UserName    string `json:"user_name" tf:"force_new"`
		DisplayName string `json:"display_name,omitempty" tf:"computed"`
		Active      bool   `json:"active,omitempty"`
		ExternalID  string `json:"external_id,omitempty" tf:"suppress_diff"`
	}
	userSchema := common.StructToSchema(entity{},
		func(m map[string]*schema.Schema) map[string]*schema.Schema {
			addEntitlementsToSchema(&m)
			m["active"].Default = true
			m["force"] = &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
			}
			m["home"] = &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			}
			m["repos"] = &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			}
			m["force_delete_repos"] = &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
			}
			m["force_delete_home_dir"] = &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
			}
			return m
		})
	scimUserFromData := func(d *schema.ResourceData) (user User, err error) {
		var u entity
		common.DataToStructPointer(d, userSchema, &u)
		return User{
			UserName:     u.UserName,
			DisplayName:  u.DisplayName,
			Active:       u.Active,
			Entitlements: readEntitlementsFromData(d),
			ExternalID:   u.ExternalID,
		}, nil
	}
	return common.Resource{
		Schema: userSchema,
		Create: func(ctx context.Context, d *schema.ResourceData, c *common.DatabricksClient) error {
			u, err := scimUserFromData(d)
			if err != nil {
				return err
			}
			usersAPI := NewUsersAPI(ctx, c)
			user, err := usersAPI.Create(u)
			if err != nil {
				return createForceOverridesManuallyAddedUser(err, d, usersAPI, u)
			}
			d.SetId(user.ID)
			return nil
		},
		Read: func(ctx context.Context, d *schema.ResourceData, c *common.DatabricksClient) error {
			user, err := NewUsersAPI(ctx, c).Read(d.Id())
			if err != nil {
				return err
			}
			d.Set("user_name", user.UserName)
			d.Set("display_name", user.DisplayName)
			d.Set("active", user.Active)
			d.Set("external_id", user.ExternalID)
			d.Set("home", fmt.Sprintf("/Users/%s", user.UserName))
			d.Set("repos", fmt.Sprintf("/Repos/%s", user.UserName))
			return user.Entitlements.readIntoData(d)
		},
		Update: func(ctx context.Context, d *schema.ResourceData, c *common.DatabricksClient) error {
			u, err := scimUserFromData(d)
			if err != nil {
				return err
			}
			return NewUsersAPI(ctx, c).Update(d.Id(), u)
		},
		Delete: func(ctx context.Context, d *schema.ResourceData, c *common.DatabricksClient) error {
			user := NewUsersAPI(ctx, c)
			userName := d.Get("user_name").(string)
			err := user.Delete(d.Id())
			if err != nil {
				return err
			}
			if c.Config.IsAccountClient() && c.Config.AccountID != "" {
				return nil
			}
			if d.Get("force_delete_repos").(bool) {
				err = workspace.NewNotebooksAPI(ctx, c).Delete(fmt.Sprintf("/Repos/%v", userName), true)
				if err != nil {
					return fmt.Errorf("force_delete_repos: %w", err)
				}
			}
			if d.Get("force_delete_home_dir").(bool) {
				err = workspace.NewNotebooksAPI(ctx, c).Delete(fmt.Sprintf("/Users/%v", userName), true)
				if err != nil {
					return fmt.Errorf("force_delete_home_dir: %w", err)
				}
			}
			return nil
		},
	}.ToResource()
}

func createForceOverridesManuallyAddedUser(err error, d *schema.ResourceData, usersAPI UsersAPI, u User) error {
	forceCreate := d.Get("force").(bool)
	if !forceCreate {
		return err
	}
	// corner-case for overriding manually provisioned users
	userName := strings.ReplaceAll(u.UserName, "'", "")
	if (err.Error() != userExistsErrorMessage(userName, false)) && (err.Error() != userExistsErrorMessage(userName, true)) {
		return err
	}
	userList, err := usersAPI.Filter(fmt.Sprintf("userName eq '%s'", userName))
	if err != nil {
		return err
	}
	if len(userList) == 0 {
		return fmt.Errorf("cannot find %s for force import", userName)
	}
	user := userList[0]
	d.SetId(user.ID)
	return usersAPI.Update(d.Id(), u)
}
