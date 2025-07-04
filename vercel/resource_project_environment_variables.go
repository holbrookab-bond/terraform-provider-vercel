package vercel

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/vercel/terraform-provider-vercel/v3/client"
)

var (
	_ resource.Resource               = &projectEnvironmentVariablesResource{}
	_ resource.ResourceWithConfigure  = &projectEnvironmentVariablesResource{}
	_ resource.ResourceWithModifyPlan = &projectEnvironmentVariablesResource{}
)

func newProjectEnvironmentVariablesResource() resource.Resource {
	return &projectEnvironmentVariablesResource{}
}

type projectEnvironmentVariablesResource struct {
	client *client.Client
}

func (r *projectEnvironmentVariablesResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_environment_variables"
}

func (r *projectEnvironmentVariablesResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

// Schema returns the schema information for a project environment variable resource.
func (r *projectEnvironmentVariablesResource) Schema(_ context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: `
Provides a resource for managing a number of Project Environment Variables.

This resource defines multiple Environment Variables on a Vercel Project.

For more detailed information, please see the [Vercel documentation](https://vercel.com/docs/concepts/projects/environment-variables).

~> Terraform currently provides this Project Environment Variables resource (multiple Environment Variables), a single Project Environment Variable Resource, and a Project resource with Environment Variables defined in-line via the ` + "`environment` field" + `.
At this time you cannot use a Vercel Project resource with in-line ` + "`environment` in conjunction with any `vercel_project_environment_variables` or `vercel_project_environment_variable`" + ` resources. Doing so will cause a conflict of settings and will overwrite Environment Variables.
`,
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{
				Required:      true,
				Description:   "The ID of the Vercel project.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"team_id": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "The ID of the Vercel team. Required when configuring a team resource if a default team has not been set in the provider.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"variables": schema.MapNestedAttribute{
				Required:    true,
				Description: "A map of Environment Variables that should be configured for the project. The map key is the environment variable name.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The ID of the Environment Variable.",
							Computed:    true,
						},
						"value": schema.StringAttribute{
							Required:    true,
							Description: "The value of the Environment Variable.",
							Sensitive:   true,
							WriteOnly:   true,
						},
						"target": schema.SetAttribute{
							Optional:    true,
							Computed:    true,
							Description: "The environments that the Environment Variable should be present on. Valid targets are either `production`, `preview`, or `development`. At least one of `target` or `custom_environment_ids` must be set.",
							ElementType: types.StringType,
							Validators: []validator.Set{
								setvalidator.ValueStringsAre(stringvalidator.OneOf("production", "preview", "development")),
								setvalidator.SizeAtLeast(1),
								setvalidator.AtLeastOneOf(
									path.MatchRelative().AtParent().AtName("custom_environment_ids"),
									path.MatchRelative().AtParent().AtName("target"),
								),
							},
						},
						"custom_environment_ids": schema.SetAttribute{
							Optional:    true,
							Computed:    true,
							ElementType: types.StringType,
							Description: "The IDs of Custom Environments that the Environment Variable should be present on. At least one of `target` or `custom_environment_ids` must be set.",
							Validators: []validator.Set{
								setvalidator.SizeAtLeast(1),
								setvalidator.AtLeastOneOf(
									path.MatchRelative().AtParent().AtName("custom_environment_ids"),
									path.MatchRelative().AtParent().AtName("target"),
								),
							},
						},
						"git_branch": schema.StringAttribute{
							Optional:    true,
							Description: "The git branch of the Environment Variable.",
						},
						"sensitive": schema.BoolAttribute{
							Description:   "Whether the Environment Variable is sensitive or not.",
							Optional:      true,
							Computed:      true,
							PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
						},
						"comment": schema.StringAttribute{
							Description: "A comment explaining what the environment variable is for.",
							Optional:    true,
							Computed:    true,
							Validators: []validator.String{
								stringvalidator.LengthBetween(0, 1000),
							},
						},
					},
				},
			},
		},
	}
}

// ProjectEnvironmentVariables reflects the state terraform stores internally for project environment variables.
type ProjectEnvironmentVariables struct {
	TeamID    types.String `tfsdk:"team_id"`
	ProjectID types.String `tfsdk:"project_id"`
	Variables types.Map    `tfsdk:"variables"`
}

func (p *ProjectEnvironmentVariables) environment(ctx context.Context) (EnvironmentItemsMap, diag.Diagnostics) {
	if p.Variables.IsNull() {
		return nil, nil
	}

	var vars EnvironmentItemsMap
	diags := p.Variables.ElementsAs(ctx, &vars, true)
	return vars, diags
}


// Updated: now takes resp *resource.ModifyPlanResponse and triggers RequiresReplace on value changes
func suppressWriteOnlyEnvVarUpdates(ctx context.Context, config *ProjectEnvironmentVariables, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) diag.Diagnostics {
    var diags diag.Diagnostics

    environment, diags := config.environment(ctx)
    if diags.HasError() {
        diags.Append(diags...)
        return diags
    }

    prefix := fmt.Sprintf("vercel_env_%s_%s_", config.ProjectID.ValueString(), config.TeamID.ValueString())

    for key, env := range environment {
        hash := sha256.Sum256([]byte(env.Value.ValueString()))
        privateKey := prefix + key
        storedHash, _ := req.Private.GetKey(ctx, privateKey)
        if len(storedHash) > 0 && strings.Trim(string(storedHash), "\"") == fmt.Sprintf("%x", hash) {
        
        } else {
			// Trigger RequiresReplace for this variable's value
			resp.RequiresReplace = append(resp.RequiresReplace, 
				path.Root("variables").AtMapKey(key).AtName("value"))
        }
    }

    return diags
}

func (r *projectEnvironmentVariablesResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var config ProjectEnvironmentVariables
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	environment, diags := config.environment(ctx)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	// Should be at least one variable
	if len(environment) == 0 {
		return
	}

	var plan ProjectEnvironmentVariables
	diags = resp.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	diags = suppressWriteOnlyEnvVarUpdates(ctx, &config, req, resp)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	/*diags = resp.Plan.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}*/

	// work out if there are any new env vars that are specifying sensitive = false
	var nonSensitiveEnvVars []path.Path
	for key, e := range environment {
		if e.ID.ValueString() != "" {
			continue
		}
		if e.Sensitive.IsUnknown() || e.Sensitive.IsNull() || e.Sensitive.ValueBool() {
			continue
		}
		nonSensitiveEnvVars = append(
			nonSensitiveEnvVars,
			path.Root("variables").
				AtMapKey(key).
				AtName("sensitive"),
		)
	}

	if len(nonSensitiveEnvVars) == 0 {
		return
	}

	// if sensitive is explicitly set to `false`, then validate that an env var can be created with the given
	// team sensitive environment variable policy.
	team, err := r.client.Team(ctx, config.TeamID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error validating project environment variables",
			"Could not validate project environment variable, unexpected error: "+err.Error(),
		)
		return
	}

	if team.SensitiveEnvironmentVariablePolicy == nil || *team.SensitiveEnvironmentVariablePolicy != "on" {
		// the policy isn't enabled
		return
	}

	for _, p := range nonSensitiveEnvVars {
		resp.Diagnostics.AddAttributeError(
			p,
			"Project Environment Variables Invalid",
			"This team has a policy that forces all environment variables to be sensitive. Please remove the `sensitive` field for your environment variables or set the `sensitive` field to `true` in your configuration.",
		)
	}
}

// EnvironmentItems represents a set of environment variables
// for use with MapNestedAttribute
type EnvironmentItemsMap map[string]EnvironmentItem

func (e *EnvironmentItemsMap) toCreateEnvironmentVariablesRequest(ctx context.Context, projectID types.String, teamID types.String) (r client.CreateEnvironmentVariablesRequest, diags diag.Diagnostics) {
	variables := []client.EnvironmentVariableRequest{}
	for key, env := range *e {
		var target []string
		diags = env.Target.ElementsAs(ctx, &target, true)
		if diags.HasError() {
			return r, diags
		}
		var customEnvironmentIDs []string
		diags = env.CustomEnvironmentIDs.ElementsAs(ctx, &customEnvironmentIDs, true)
		if diags.HasError() {
			return r, diags
		}
		var envVariableType string
		if env.Sensitive.ValueBool() {
			envVariableType = "sensitive"
		} else {
			envVariableType = "encrypted"
		}
		variables = append(variables, client.EnvironmentVariableRequest{
			Key:                  key,
			Value:                env.Value.ValueString(),
			Target:               target,
			CustomEnvironmentIDs: customEnvironmentIDs,
			Type:                 envVariableType,
			GitBranch:            env.GitBranch.ValueStringPointer(),
			Comment:              env.Comment.ValueString(),
		})
	}

	return client.CreateEnvironmentVariablesRequest{
		ProjectID:            projectID.ValueString(),
		TeamID:               teamID.ValueString(),
		EnvironmentVariables: variables,
	}, nil
}

// convertResponseToProjectEnvironmentVariables is used to populate terraform state based on an API response.
// Where possible, values from the API response are used to populate state. If not possible,
// values from plan are used.
func convertResponseToProjectEnvironmentVariables(
	ctx context.Context,
	response []client.EnvironmentVariable,
	plan ProjectEnvironmentVariables,
	unchanged EnvironmentItemsMap,
) (ProjectEnvironmentVariables, diag.Diagnostics) {
	environment, diags := plan.environment(ctx)
	if diags.HasError() {
		return ProjectEnvironmentVariables{}, diags
	}

	// Build a map of environment variables keyed by variable name
	env := make(map[string]attr.Value)
	alreadyPresent := map[string]struct{}{}
	for _, e := range response {
		var targetValue attr.Value
		if len(e.Target) > 0 {
			target := make([]attr.Value, 0, len(e.Target))
			for _, t := range e.Target {
				target = append(target, types.StringValue(t))
			}
			targetValue = types.SetValueMust(types.StringType, target)
		} else {
			targetValue = types.SetNull(types.StringType)
		}

		var customEnvIDsValue attr.Value
		if len(e.CustomEnvironmentIDs) > 0 {
			customEnvIDs := make([]attr.Value, 0, len(e.CustomEnvironmentIDs))
			for _, c := range e.CustomEnvironmentIDs {
				customEnvIDs = append(customEnvIDs, types.StringValue(c))
			}
			customEnvIDsValue = types.SetValueMust(types.StringType, customEnvIDs)
		} else {
			customEnvIDsValue = types.SetNull(types.StringType)
		}
		value := types.StringValue(e.Value)
		if e.Type == "sensitive" {
			value = types.StringNull()
		}
		if e.Decrypted != nil && !*e.Decrypted || e.Type == "sensitive" {
			for key, p := range environment {
				var target []string
				diags := p.Target.ElementsAs(ctx, &target, true)
				if diags.HasError() {
					return ProjectEnvironmentVariables{}, diags
				}
				var customEnvironmentIDs []string
				diags = p.CustomEnvironmentIDs.ElementsAs(ctx, &customEnvironmentIDs, true)
				if diags.HasError() {
					return ProjectEnvironmentVariables{}, diags
				}
				if key == e.Key && isSameStringSet(target, e.Target) && isSameStringSet(customEnvironmentIDs, e.CustomEnvironmentIDs) {
					value = p.Value
					break
				}
			}
		}

		// The Vercel API returns duplicate environment variables, so we need to filter them out.
		if _, ok := alreadyPresent[e.ID]; ok {
			continue
		}
		alreadyPresent[e.ID] = struct{}{}

		// Use the env var key as the map key
		env[e.Key] = types.ObjectValueMust(
			EnvVariableElemType.AttrTypes,
			map[string]attr.Value{
				"value":                  value,
				"target":                 targetValue,
				"custom_environment_ids": customEnvIDsValue,
				"git_branch":             types.StringPointerValue(e.GitBranch),
				"id":                     types.StringValue(e.ID),
				"sensitive":              types.BoolValue(e.Type == "sensitive"),
				"comment":                types.StringValue(e.Comment),
			},
		)
	}

	// Add unchanged items to the map (by key)
	for key, e := range unchanged {
		if _, exists := env[key]; !exists {
			env[key] = e.toAttrValue()
		}
	}

	// No need to sort, as maps are order-insensitive

	return ProjectEnvironmentVariables{
		TeamID:    toTeamID(plan.TeamID.ValueString()),
		ProjectID: plan.ProjectID,
		Variables: types.MapValueMust(EnvVariableElemType, env),
	}, nil
}

// Create will create a new project environment variable for a Vercel project.
// This is called automatically by the provider when a new resource should be created.
func (r *projectEnvironmentVariablesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ProjectEnvironmentVariables
	diags := req.Config.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.GetProject(ctx, plan.ProjectID.ValueString(), plan.TeamID.ValueString())
	if client.NotFound(err) {
		resp.Diagnostics.AddError(
			"Error creating project environment variables",
			"Could not find project, please make sure both the project_id and team_id match the project and team you wish to deploy to.",
		)
		return
	}

	envs, diags := plan.environment(ctx)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	request, diags := envs.toCreateEnvironmentVariablesRequest(ctx, plan.ProjectID, plan.TeamID)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	created, err := r.client.CreateEnvironmentVariables(ctx, request)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating project environment variables",
			"Could not create project environment variables, unexpected error: "+err.Error(),
		)
	}

	result, diags := convertResponseToProjectEnvironmentVariables(ctx, created, plan, nil)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	// Set the hash of the environment variable values in the private state.
	prefix := fmt.Sprintf("vercel_env_%s_%s_", plan.ProjectID.ValueString(), plan.TeamID.ValueString())
	for key, env := range envs { 
		hash := sha256.Sum256([]byte(env.Value.ValueString()))
		privateKey := prefix + key
		resp.Private.SetKey(ctx, privateKey, []byte(fmt.Sprintf("\"%x\"", hash)))
	}

	tflog.Info(ctx, "created project environment variables", map[string]any{
		"team_id":    result.TeamID.ValueString(),
		"project_id": result.ProjectID.ValueString(),
	})

	diags = resp.State.Set(ctx, result)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Read will read an environment variable of a Vercel project by requesting it from the Vercel API, and will update terraform
// with this information.
func (r *projectEnvironmentVariablesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ProjectEnvironmentVariables
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	existing, diags := state.environment(ctx)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	existingIDs := map[string]struct{}{}
	for _, e := range existing {
		if e.ID.ValueString() != "" {
			existingIDs[e.ID.ValueString()] = struct{}{}
		}
	}
	if len(existingIDs) == 0 {
		// no existing environment variables, nothing to do
		return
	}

	envs, err := r.client.GetEnvironmentVariables(ctx, state.ProjectID.ValueString(), state.TeamID.ValueString())
	if client.NotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading project environment variables",
			"Could not read environment variables, unexpected error: "+err.Error(),
		)
		return
	}

	var toUse []client.EnvironmentVariable
	for _, e := range envs {
		if _, ok := existingIDs[e.ID]; ok {
			// This ID exists in the env vars we have already. So use it.
			toUse = append(toUse, e)
		}
	}
	for _, e := range envs {
		if _, ok := existingIDs[e.ID]; !ok {
			// The env var exists at the moment, but not in TF state (the ID isn't present).
			// Check if it has the same `key`, `target` and `custom_environment_ids` as an existing env var.
			// This detects drift for stuff like: deleting an env var and then creating it again (the ID changes).
			for key, ee := range existing {
				var target []string
				diags := ee.Target.ElementsAs(ctx, &target, true)
				if diags.HasError() {
					resp.Diagnostics.Append(diags...)
					return
				}
				var customEnvironmentIDs []string
				diags = ee.CustomEnvironmentIDs.ElementsAs(ctx, &customEnvironmentIDs, true)
				if diags.HasError() {
					resp.Diagnostics.Append(diags...)
					return
				}
				if key == e.Key && isSameStringSet(target, e.Target) && isSameStringSet(customEnvironmentIDs, e.CustomEnvironmentIDs) {
					toUse = append(toUse, e)
				}
			}
		}
	}

	result, diags := convertResponseToProjectEnvironmentVariables(ctx, toUse, state, nil)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	tflog.Info(ctx, "read project environment variables", map[string]any{
		"team_id":    result.TeamID.ValueString(),
		"project_id": result.ProjectID.ValueString(),
	})

	diags = resp.State.Set(ctx, result)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Update updates the project environment variable of a Vercel project state.
func (r *projectEnvironmentVariablesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ProjectEnvironmentVariables
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state ProjectEnvironmentVariables
	diags = req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	stateEnvs, diags := state.environment(ctx)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	var config ProjectEnvironmentVariables
	diags = req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	configEnvs, diags := config.environment(ctx)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}


	// Update the hash of all the environment variable values in the private state.
	prefix := fmt.Sprintf("vercel_env_%s_%s_", plan.ProjectID.ValueString(), plan.TeamID.ValueString())
	for key, env := range configEnvs { 
		hash := sha256.Sum256([]byte(env.Value.ValueString()))
		privateKey := prefix + key
		resp.Private.SetKey(ctx, privateKey, []byte(fmt.Sprintf("\"%x\"", hash)))
	}

	planEnvs, diags := plan.environment(ctx)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	envsFromAPI, err := r.client.GetEnvironmentVariables(ctx, state.ProjectID.ValueString(), state.TeamID.ValueString())
	if client.NotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading project environment variables as part of environment variable update",
			"Could not read environment variables as part of updating, unexpected error: "+err.Error(),
		)
		return
	}

	// Build a map of envs from API for efficient lookup by key
	envsFromAPIMap := make(map[string]client.EnvironmentVariable, len(envsFromAPI))
	for _, e := range envsFromAPI {
		envsFromAPIMap[e.Key] = e
	}
	
	toAdd := make(EnvironmentItemsMap)
	for key := range planEnvs {
		_, ok := envsFromAPIMap[key]
		if !ok {
			toAdd[key] = configEnvs[key]
		}
	}
	
	toRemove := make(EnvironmentItemsMap)
	unchanged := make(EnvironmentItemsMap)
	for key, e := range stateEnvs {
		_, ok := planEnvs[key]
		if !ok {
			// If a value isn't in in the planned state, it means it was removed from the config.
			toRemove[key] = e

			// As this is fully deleted, remove the hash from the private state.
			privateKey := prefix + key
			resp.Private.SetKey(ctx, privateKey, nil)
			continue
		}
		apiEnv, ok := envsFromAPIMap[key]
		if ok && (e.ID.ValueString() != apiEnv.ID || !envVarMatches(ctx, key, configEnvs[key], apiEnv)) {
			toRemove[key] = e
			toAdd[key] = configEnvs[key]
			continue
		}
		unchanged[key] = e
	}

	tflog.Info(ctx, "Updating environment variables", map[string]any{
		"to_remove": len(toRemove),
		"to_add":    len(toAdd),
		"unchanged": len(unchanged),
	})

	for key, v := range toRemove {
		err := r.client.DeleteEnvironmentVariable(ctx, state.ProjectID.ValueString(), state.TeamID.ValueString(), v.ID.ValueString())
		if client.NotFound(err) {
			continue
		}
		if err != nil {
			resp.Diagnostics.AddError(
				"Error updating Project Environment Variables",
				fmt.Sprintf(
					"Could not remove environment variable %s (%s), unexpected error: %s",
					key,
					v.ID.ValueString(),
					err,
				),
			)
			return
		}

		tflog.Info(ctx, "deleted environment variable", map[string]any{
			"team_id":        plan.TeamID.ValueString(),
			"project_id":     plan.ProjectID.ValueString(),
			"environment_id": v.ID.ValueString(),
		})
	}

	var response []client.EnvironmentVariable
	if len(toAdd) > 0 {
		if len(toRemove) > 0 {
			// Sleep a bit to ensure the environment variables are fully propagated before we try to create them
			// This is disgusting, but what you gonna do?
			time.Sleep(time.Second * 5)
		}
		request, diags := toAdd.toCreateEnvironmentVariablesRequest(ctx, plan.ProjectID, plan.TeamID)

		if diags.HasError() {
			resp.Diagnostics.Append(diags...)
			return
		}
		response, err = r.client.CreateEnvironmentVariables(ctx, request)
		if err != nil {
			resp.Diagnostics.AddError(
				"Error updating project environment variables",
				"Could not update project environment variable, unexpected error: "+err.Error(),
			)
			return
		}
	}

	result, diags := convertResponseToProjectEnvironmentVariables(ctx, response, plan, unchanged)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	tflog.Info(ctx, "updated project environment variables", map[string]any{
		"team_id":    result.TeamID.ValueString(),
		"project_id": result.ProjectID.ValueString(),
	})

	diags = resp.State.Set(ctx, result)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Delete deletes a Vercel project environment variable.
func (r *projectEnvironmentVariablesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ProjectEnvironmentVariables
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	envs, diags := state.environment(ctx)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	for key, v := range envs {
		err := r.client.DeleteEnvironmentVariable(ctx, state.ProjectID.ValueString(), state.TeamID.ValueString(), v.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError(
				"Error updating Project Environment Variables",
				fmt.Sprintf(
					"Could not remove environment variable %s (%s), unexpected error: %s",
					key,
					v.ID.ValueString(),
					err,
				),
			)
			return
		}
		tflog.Info(ctx, "deleted environment variable", map[string]any{
			"team_id":        state.TeamID.ValueString(),
			"project_id":     state.ProjectID.ValueString(),
			"environment_id": v.ID.ValueString(),
		})
	}
}

// envVarMatches returns true if the two environment variables match by key, target, and custom_environment_ids.
func envVarMatches(ctx context.Context, key string, ee EnvironmentItem, e client.EnvironmentVariable) bool {
	// TODO: Incorporate any data changes if the value in Vercel has updated, and we can actually read it.
	
	var target []string
	diags := ee.Target.ElementsAs(ctx, &target, true)
	if diags.HasError() {
		return false
	}
	var customEnvironmentIDs []string
	diags = ee.CustomEnvironmentIDs.ElementsAs(ctx, &customEnvironmentIDs, true)
	if diags.HasError() {
		return false
	}
	if (key == e.Key && isSameStringSet(target, e.Target) && isSameStringSet(customEnvironmentIDs, e.CustomEnvironmentIDs)) {
		if e.Decrypted != nil && !*e.Decrypted {
			return false // We don't know if it's value is encrypted.
		}
		if e.Type == "sensitive" {
			return false // We don't know if it's the same env var if sensitive
		}
		if e.Value != ee.Value.ValueString() {
			return false // Value mismatches, so we need to update it.
		}
		return true
	}
	return false
}
