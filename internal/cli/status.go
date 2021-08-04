package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/posener/complete"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hashicorp/waypoint-plugin-sdk/terminal"
	"github.com/hashicorp/waypoint/internal/clicontext"
	"github.com/hashicorp/waypoint/internal/clierrors"
	"github.com/hashicorp/waypoint/internal/pkg/flag"
	pb "github.com/hashicorp/waypoint/internal/server/gen"
)

type StatusCommand struct {
	*baseCommand

	flagContextName string
	flagVerbose     bool
	flagJson        bool
	flagAllProjects bool
	filterFlags     filterFlags

	serverCtx *clicontext.Config
}

func (c *StatusCommand) Run(args []string) int {
	flagSet := c.Flags()
	// Initialize. If we fail, we just exit since Init handles the UI.
	if err := c.Init(
		WithArgs(args),
		WithFlags(flagSet),
		WithConfig(true), // optional config loading
	); err != nil {
		return 1
	}

	var ctxName string
	defaultName, err := c.contextStorage.Default()
	if err != nil {
		c.ui.Output(
			"Error getting default context: %s",
			clierrors.Humanize(err),
			terminal.WithErrorStyle(),
		)
		return 1
	}
	ctxName = defaultName

	ctxConfig, err := c.contextStorage.Load(ctxName)
	if err != nil {
		c.ui.Output("Error loading context %q: %s", ctxName, err.Error(), terminal.WithErrorStyle())
		return 1
	}
	c.serverCtx = ctxConfig

	cmdArgs := flagSet.Args()

	if len(cmdArgs) > 1 {
		c.ui.Output("No more than 1 argument required.\n\n"+c.Help(), terminal.WithErrorStyle())
		return 1
	}

	// Determine which view to show based on user input
	var projectTarget, appTarget string
	if len(cmdArgs) >= 1 {
		match := reAppTarget.FindStringSubmatch(cmdArgs[0])

		if match != nil {
			projectTarget = match[1]
			appTarget = match[2]
		} else {
			projectTarget = cmdArgs[0]
		}
	} else if len(cmdArgs) == 0 {
		// If we're in a project dir, load the name. Otherwise we'll
		// show a list of all projects and their status and leave projectTarget
		// blank
		if c.project.Ref() != nil {
			projectTarget = c.project.Ref().Project
		}
	}

	if appTarget == "" && c.flagApp != "" {
		appTarget = c.flagApp
	} else if appTarget != "" && c.flagApp != "" {
		// setting app target and passing the flag app is a collision
		c.ui.Output(wpAppFlagAndTargetIncludedMsg, terminal.WithWarningStyle())
	}

	// Generate a status view
	if projectTarget == "" || c.flagAllProjects {
		// Show high-level status of all projects
		err = c.FormatProjectStatus()
		if err != nil {
			c.ui.Output("CLI failed to build project statuses: "+clierrors.Humanize(err), terminal.WithErrorStyle())
			return 1
		}
	} else if projectTarget != "" && appTarget == "" {
		// Show status of apps inside project
		err = c.FormatProjectAppStatus(projectTarget)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				c.ui.Output(wpProjectNotFound, projectTarget, c.serverCtx.Server.Address, terminal.WithErrorStyle())
			} else {
				c.ui.Output("CLI failed to format project app statuses:"+clierrors.Humanize(err), terminal.WithErrorStyle())
			}
			return 1
		}
	} else if projectTarget != "" && appTarget != "" {
		// Advanced view of a single app status
		err = c.FormatAppStatus(projectTarget, appTarget)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				c.ui.Output(wpAppNotFound, appTarget, projectTarget, c.serverCtx.Server.Address, terminal.WithErrorStyle())
			} else {
				c.ui.Output("CLI failed to format app status:"+clierrors.Humanize(err), terminal.WithErrorStyle())
			}
			return 1
		}
	}

	return 0
}

// FormatProjectAppStatus formats all applications inside a project
func (c *StatusCommand) FormatProjectAppStatus(projectTarget string) error {
	if !c.flagJson {
		c.ui.Output(wpStatusProjectMsg, projectTarget, c.serverCtx.Server.Address)
	}

	// Get our API client
	client := c.project.Client()

	resp, err := client.GetProject(c.Ctx, &pb.GetProjectRequest{
		Project: &pb.Ref_Project{
			Project: projectTarget,
		},
	})
	if err != nil {
		return err
	}
	project := resp.Project

	workspace, err := c.getWorkspaceFromProject(resp)
	if err != nil {
		return err
	}

	// Summary
	//   App list

	appHeaders := []string{
		"App", "Workspace", "Latest Status", "Last Check",
	}

	appTbl := terminal.NewTable(appHeaders...)

	appFailures := false
	for _, app := range project.Applications {
		appStatusResp, err := client.GetLatestStatusReport(c.Ctx, &pb.GetLatestStatusReportRequest{
			Application: &pb.Ref_Application{
				Application: app.Name,
				Project:     project.Name,
			},
			Workspace: &pb.Ref_Workspace{
				Workspace: workspace,
			},
		})
		if status.Code(err) == codes.NotFound {
			// App doesn't have a status report yet, likely not deployed
			err = nil
		}
		if err != nil {
			return err
		}

		statusReportComplete, statusReportCheckTime, err := c.FormatStatusReportComplete(appStatusResp)
		if err != nil {
			return err
		}

		statusColor := ""
		columns := []string{
			app.Name,
			workspace,
			statusReportComplete,
			statusReportCheckTime,
		}

		// Add column data to table
		appTbl.Rich(
			columns,
			[]string{
				statusColor,
			},
		)
	}

	if c.flagJson {
		c.outputJsonProjectAppStatus(appTbl, project)
	} else {
		c.ui.Output("")
		c.ui.Table(appTbl, terminal.WithStyle("Simple"))
		c.ui.Output("")
		c.ui.Output(wpStatusProjectSuccessMsg)
	}

	if appFailures {
		c.ui.Output("")

		c.ui.Output(wpStatusHealthTriageMsg, projectTarget, terminal.WithWarningStyle())
	}

	return nil
}

func (c *StatusCommand) FormatAppStatus(projectTarget string, appTarget string) error {
	if !c.flagJson {
		c.ui.Output(wpStatusAppProjectMsg, appTarget, projectTarget, c.serverCtx.Server.Address)
	}

	// Get our API client
	client := c.project.Client()

	projResp, err := client.GetProject(c.Ctx, &pb.GetProjectRequest{
		Project: &pb.Ref_Project{
			Project: projectTarget,
		},
	})
	if err != nil {
		return err
	}
	project := projResp.Project

	workspace, err := c.getWorkspaceFromProject(projResp)
	if err != nil {
		return err
	}

	// App Summary
	//  Summary of single app
	var app *pb.Application
	for _, a := range project.Applications {
		if a.Name == appTarget {
			app = a
			break
		}
	}
	if app == nil {
		return fmt.Errorf("Did not find aplication %q in project %q", appTarget, projectTarget)
	}

	// Deployment Summary
	//   Deployment List

	respDeployList, err := client.ListDeployments(c.Ctx, &pb.ListDeploymentsRequest{
		Application: &pb.Ref_Application{
			Application: app.Name,
			Project:     project.Name,
		},
		Workspace: &pb.Ref_Workspace{
			Workspace: workspace,
		},
		LoadDetails: pb.Deployment_BUILD,
	})
	if err != nil {
		return err
	}

	// Pregrab status report list
	statusReportsResp, err := client.ListStatusReports(c.Ctx, &pb.ListStatusReportsRequest{
		Application: &pb.Ref_Application{
			Application: app.Name,
			Project:     project.Name,
		},
		Workspace: &pb.Ref_Workspace{
			Workspace: workspace,
		},
	})
	if status.Code(err) == codes.NotFound || status.Code(err) == codes.Unimplemented {
		err = nil
		statusReportsResp = nil
	}
	if err != nil {
		return err
	}

	deployHeaders := []string{
		"App Name", "Version", "Workspace", "Platform", "Details", "Lifecycle State",
	}

	deployTbl := terminal.NewTable(deployHeaders...)

	resourcesHeaders := []string{
		"Type", "Platform", "Category",
	}

	resourcesTbl := terminal.NewTable(resourcesHeaders...)

	deployStatusReportComplete := "N/A"
	var deployStatusReportCheckTime string
	appFailures := false // actually check this
	if len(respDeployList.Deployments) > 0 {
		deploy := respDeployList.Deployments[0]
		statusColor := ""

		var details string
		if img, ok := deploy.Preload.Build.Labels["common/image-id"]; ok {
			img = shortImg(img)

			details = "image:" + img
		}

		columns := []string{
			deploy.Application.Application,
			fmt.Sprintf("v%d", deploy.Sequence),
			deploy.Workspace.Workspace,
			deploy.Component.Name,
			details,
			deploy.Status.State.String(),
		}

		// Add column data to table
		deployTbl.Rich(
			columns,
			[]string{
				statusColor,
			},
		)

		// get deploy status report
		statusReport, err := c.getLatestStatusReportByDeployId(statusReportsResp, deploy.Id)
		if status.Code(err) == codes.NotFound {
			err = nil
			statusReport = nil
		}
		if err != nil {
			return err
		}
		deployStatusReportComplete, deployStatusReportCheckTime, err = c.FormatStatusReportComplete(statusReport)
		if err != nil {
			return err
		}

		// Deployment Resources Summary
		//   Resources List
		// TODO(briancain): Add resource health when it exists
		if statusReport != nil {
			for _, dr := range statusReport.Resources {
				columns := []string{
					dr.Name,
					dr.Platform,
					dr.CategoryDisplayHint.String(),
				}

				// Add column data to table
				resourcesTbl.Rich(
					columns,
					[]string{
						statusColor,
					},
				)
			}
		}

	} // else show no table

	// Release Summary
	//   Release List

	release, err := client.GetLatestRelease(c.Ctx, &pb.GetLatestReleaseRequest{
		Application: &pb.Ref_Application{
			Application: app.Name,
			Project:     project.Name,
		},
		Workspace: &pb.Ref_Workspace{
			Workspace: workspace,
		},
		LoadDetails: pb.Release_BUILD,
	})
	if status.Code(err) == codes.NotFound {
		err = nil
		release = nil
	}
	if err != nil {
		return err
	}

	// Same headers as deploy
	releaseTbl := terminal.NewTable(deployHeaders...)
	releaseResourcesTbl := terminal.NewTable(resourcesHeaders...)

	releaseUnimplemented := true
	releaseStatusReportComplete := "N/A"
	var releaseStatusReportCheckTime string
	if release != nil {
		releaseUnimplemented = release.Unimplemented
		if !release.Unimplemented {
			statusColor := ""

			var details []string
			if img, ok := release.Preload.Build.Labels["common/image-id"]; ok {
				img = shortImg(img)

				details = append(details, "image:"+img)
			}

			columns := []string{
				release.Application.Application,
				fmt.Sprintf("v%d", release.Sequence),
				release.Workspace.Workspace,
				release.Component.Name,
				details[0],
				release.Status.State.String(),
			}

			// Add column data to table
			releaseTbl.Rich(
				columns,
				[]string{
					statusColor,
				},
			)

			statusReport, err := c.getLatestStatusReportByReleaseId(statusReportsResp, release.Id)
			if status.Code(err) == codes.NotFound {
				err = nil
				statusReport = nil
			}
			if err != nil {
				return err
			}
			releaseStatusReportComplete, releaseStatusReportCheckTime, err = c.FormatStatusReportComplete(statusReport)
			if err != nil {
				return err
			}

			// Deployment Resources Summary
			//   Resources List
			for _, rr := range release.DeclaredResources {
				columns := []string{
					rr.Name,
					rr.Platform,
					rr.CategoryDisplayHint.String(),
				}

				// Add column data to table
				releaseResourcesTbl.Rich(
					columns,
					[]string{
						statusColor,
					},
				)
			}

		}
	} // else show no table

	appHeaders := []string{
		"App", "Workspace", "Deployment Status", "Deployment Checked", "Release Status", "Release Checked",
	}

	appTbl := terminal.NewTable(appHeaders...)

	statusColor := ""
	columns := []string{
		app.Name,
		workspace,
		deployStatusReportComplete,
		deployStatusReportCheckTime,
		releaseStatusReportComplete,
		releaseStatusReportCheckTime,
	}

	// Add column data to table
	appTbl.Rich(
		columns,
		[]string{
			statusColor,
		},
	)

	// TODO(briancain): we don't yet store a list of recent events per app
	// but it would go here if we did.
	// Recent Events
	//   Events List

	if c.flagJson {
		c.outputJsonAppStatus(appTbl, deployTbl, resourcesTbl, releaseTbl, releaseResourcesTbl, project)
	} else {
		c.ui.Output("")
		c.ui.Output("Application Summary")
		c.ui.Table(appTbl, terminal.WithStyle("Simple"))
		c.ui.Output("")
		c.ui.Output("Deployment Summary")
		c.ui.Table(deployTbl, terminal.WithStyle("Simple"))
		c.ui.Output("")
		c.ui.Output("Deployment Resources Summary")
		c.ui.Table(resourcesTbl, terminal.WithStyle("Simple"))
		c.ui.Output("")

		if !releaseUnimplemented {
			c.ui.Output("Release Summary")
			c.ui.Table(releaseTbl, terminal.WithStyle("Simple"))
			c.ui.Output("")
			c.ui.Output("Release Resources Summary")
			c.ui.Table(releaseResourcesTbl, terminal.WithStyle("Simple"))
			c.ui.Output("")
		}

		c.ui.Output(wpStatusAppSuccessMsg)
	}

	if appFailures {
		c.ui.Output("")

		c.ui.Output(wpStatusHealthTriageMsg, projectTarget, terminal.WithWarningStyle())
	}

	return nil
}

// FormatProjectStatus formats all known projects into a table
func (c *StatusCommand) FormatProjectStatus() error {
	if !c.flagJson {
		c.ui.Output(wpStatusMsg, c.serverCtx.Server.Address)
	}

	// Get our API client
	client := c.project.Client()

	projectResp, err := client.ListProjects(c.Ctx, &empty.Empty{})
	if err != nil {
		c.ui.Output("Failed to retrieve all projects:"+clierrors.Humanize(err), terminal.WithErrorStyle())
		return err
	}
	projNameList := projectResp.Projects

	headers := []string{
		"Project", "Workspace", "App Statuses",
	}

	tbl := terminal.NewTable(headers...)

	for _, projectRef := range projNameList {
		resp, err := client.GetProject(c.Ctx, &pb.GetProjectRequest{
			Project: projectRef,
		})
		if err != nil {
			return err
		}

		workspace, err := c.getWorkspaceFromProject(resp)
		if err != nil {
			return err
		}

		// Get App Statuses
		var appStatusReports []*pb.StatusReport
		var ready, alive, down, unknown int
		for _, app := range resp.Project.Applications {
			appStatusResp, err := client.GetLatestStatusReport(c.Ctx, &pb.GetLatestStatusReportRequest{
				Application: &pb.Ref_Application{
					Application: app.Name,
					Project:     resp.Project.Name,
				},
				Workspace: &pb.Ref_Workspace{
					Workspace: workspace,
				},
			})
			if status.Code(err) == codes.NotFound {
				// App doesn't have a status report yet, likely not deployed
				err = nil
				continue
			}
			if err != nil {
				return err
			}

			switch appStatusResp.Health.HealthStatus {
			case "DOWN":
				down++
			case "UNKNOWN":
				unknown++
			case "READY":
				ready++
			case "ALIVE":
				alive++
			}
			appStatusReports = append(appStatusReports, appStatusResp)
		}

		statusReportComplete := "N/A"

		if len(appStatusReports) != 0 {
			statusReportComplete = ""
			if ready > 0 {
				statusReportComplete = statusReportComplete + fmt.Sprintf("%v READY ", ready)
			}
			if alive > 0 {
				statusReportComplete = statusReportComplete + fmt.Sprintf("%v ALIVE ", alive)
			}
			if down > 0 {
				statusReportComplete = statusReportComplete + fmt.Sprintf("%v DOWN ", down)
			}
			if alive > 0 {
				statusReportComplete = statusReportComplete + fmt.Sprintf("%v UNKNOWN ", unknown)
			}
		}

		statusColor := ""
		columns := []string{
			resp.Project.Name,
			workspace,
			statusReportComplete, // app statuses overall
		}

		// Add column data to table
		tbl.Rich(
			columns,
			[]string{
				statusColor,
			},
		)
	}

	// TODO: Sort by Name, Workspace, or Status
	// might have to pre-sort by status since strings are ascii

	// Render the table
	if c.flagJson {
		c.outputJsonProjectStatus(tbl)
	} else {
		c.ui.Output("")
		c.ui.Table(tbl, terminal.WithStyle("Simple"))
		c.ui.Output("")
		c.ui.Output(wpStatusSuccessMsg)
	}

	return nil
}

func (c *StatusCommand) outputJsonProjectStatus(t *terminal.Table) error {
	var output []map[string]interface{}

	// Add server context
	serverContext := map[string]interface{}{}
	serverContext["Address"] = c.serverCtx.Server.Address
	serverContext["ServerPlatform"] = c.serverCtx.Server.Platform

	sc := map[string]interface{}{"ServerContext": serverContext}
	output = append(output, sc)

	p := []map[string]interface{}{}
	for _, row := range t.Rows {
		c := map[string]interface{}{}

		for j, r := range row {
			// Remove any whitespacess in key
			header := strings.ReplaceAll(t.Headers[j], " ", "")
			c[header] = r.Value
		}
		p = append(p, c)
	}

	ps := map[string]interface{}{"Projects": p}
	output = append(output, ps)

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}

	c.ui.Output(string(data))

	return nil
}

func (c *StatusCommand) outputJsonProjectAppStatus(
	t *terminal.Table,
	project *pb.Project,
) error {
	var output []map[string]interface{}

	// Add server context
	serverContext := map[string]interface{}{}
	serverContext["Address"] = c.serverCtx.Server.Address
	serverContext["ServerPlatform"] = c.serverCtx.Server.Platform

	sc := map[string]interface{}{"ServerContext": serverContext}
	output = append(output, sc)

	// Add project info
	projectInfo := map[string]interface{}{}
	projectInfo["Name"] = project.Name

	pc := map[string]interface{}{"Project": projectInfo}
	output = append(output, pc)

	a := []map[string]interface{}{}
	for _, row := range t.Rows {
		c := map[string]interface{}{}

		for j, r := range row {
			// Remove any whitespacess in key
			header := strings.ReplaceAll(t.Headers[j], " ", "")
			c[header] = r.Value
		}
		a = append(a, c)
	}

	ps := map[string]interface{}{"Applications": a}
	output = append(output, ps)

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}

	c.ui.Output(string(data))

	return nil
}

func (c *StatusCommand) outputJsonAppStatus(
	appTbl *terminal.Table,
	deployTbl *terminal.Table,
	resourcesTbl *terminal.Table,
	releaseTbl *terminal.Table,
	releaseResourcesTbl *terminal.Table,
	project *pb.Project,
) error {
	var output []map[string]interface{}

	// Add server context
	serverContext := map[string]interface{}{}
	serverContext["Address"] = c.serverCtx.Server.Address
	serverContext["ServerPlatform"] = c.serverCtx.Server.Platform

	sc := map[string]interface{}{"ServerContext": serverContext}
	output = append(output, sc)

	// Add project info
	projectInfo := map[string]interface{}{}
	projectInfo["Name"] = project.Name

	pc := map[string]interface{}{"Project": projectInfo}
	output = append(output, pc)

	a := []map[string]interface{}{}
	for _, row := range appTbl.Rows {
		c := map[string]interface{}{}

		for j, r := range row {
			// Remove any whitespacess in key
			header := strings.ReplaceAll(appTbl.Headers[j], " ", "")
			c[header] = r.Value
		}
		a = append(a, c)
	}

	ps := map[string]interface{}{"Applications": a}
	output = append(output, ps)

	d := []map[string]interface{}{}
	for _, row := range deployTbl.Rows {
		c := map[string]interface{}{}

		for j, r := range row {
			// Remove any whitespacess in key
			header := strings.ReplaceAll(deployTbl.Headers[j], " ", "")
			c[header] = r.Value
		}
		d = append(d, c)
	}

	ds := map[string]interface{}{"DeploymentSummary": d}
	output = append(output, ds)

	dr := []map[string]interface{}{}
	for _, row := range resourcesTbl.Rows {
		c := map[string]interface{}{}

		for j, r := range row {
			// Remove any whitespacess in key
			header := strings.ReplaceAll(resourcesTbl.Headers[j], " ", "")
			c[header] = r.Value
		}
		dr = append(dr, c)
	}

	drs := map[string]interface{}{"DeploymentResourcesSummary": dr}
	output = append(output, drs)

	rs := []map[string]interface{}{}
	for _, row := range releaseTbl.Rows {
		c := map[string]interface{}{}

		for j, r := range row {
			// Remove any whitespacess in key
			header := strings.ReplaceAll(releaseTbl.Headers[j], " ", "")
			c[header] = r.Value
		}
		rs = append(rs, c)
	}

	rsj := map[string]interface{}{"ReleasesSummary": rs}
	output = append(output, rsj)

	rr := []map[string]interface{}{}
	for _, row := range releaseResourcesTbl.Rows {
		c := map[string]interface{}{}

		for j, r := range row {
			// Remove any whitespacess in key
			header := strings.ReplaceAll(releaseResourcesTbl.Headers[j], " ", "")
			c[header] = r.Value
		}
		rr = append(rr, c)
	}

	rrs := map[string]interface{}{"ReleaseResourcesSummary": rr}
	output = append(output, rrs)

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}

	c.ui.Output(string(data))

	return nil
}

// Status Helpers

func (c *StatusCommand) FormatStatusReportComplete(
	statusReport *pb.StatusReport,
) (string, string, error) {
	statusReportComplete := "N/A"

	if statusReport == nil {
		return statusReportComplete, "", nil
	}

	switch statusReport.Health.HealthStatus {
	case "READY":
		statusReportComplete = "✔ READY"
	case "ALIVE":
		statusReportComplete = "✔ ALIVE"
	case "DOWN":
		statusReportComplete = "✖ DOWN"
	case "PARTIAL":
		statusReportComplete = "● PARTIAL"
	case "UNKNOWN":
		statusReportComplete = "? UNKNOWN"
	}

	t, err := ptypes.Timestamp(statusReport.GeneratedTime)
	if err != nil {
		return statusReportComplete, "", err
	}

	return statusReportComplete, humanize.Time(t), nil
}

func (c *StatusCommand) getWorkspaceFromProject(pr *pb.GetProjectResponse) (string, error) {
	var workspace string

	if len(pr.Workspaces) != 0 {
		if c.flagWorkspace != "" {
			for _, ws := range pr.Workspaces {
				if ws.Workspace.Workspace == c.flagWorkspace {
					workspace = ws.Workspace.Workspace
					break
				}
			}

			if workspace == "" {
				return "", fmt.Errorf("Failed to find project in requested workspace %q", c.flagWorkspace)
			}
		} else {
			// No workspace flag specified, try the "first" one
			workspace = pr.Workspaces[0].Workspace.Workspace
		}
	}

	return workspace, nil
}

func (c *StatusCommand) getLatestStatusReportByDeployId(
	statusReportsResp *pb.ListStatusReportsResponse,
	deployId string,
) (*pb.StatusReport, error) {
	for _, statusReport := range statusReportsResp.StatusReports {
		if deploymentTargetId, ok := statusReport.TargetId.(*pb.StatusReport_DeploymentId); ok {
			if deploymentTargetId.DeploymentId == deployId {
				return statusReport, nil
			}
		}
	}

	return nil, status.Errorf(codes.NotFound, "Failed to find associated Status Report by deployment id %q", deployId)
}

func (c *StatusCommand) getLatestStatusReportByReleaseId(
	statusReportsResp *pb.ListStatusReportsResponse,
	releaseId string,
) (*pb.StatusReport, error) {
	for _, statusReport := range statusReportsResp.StatusReports {
		if releaseTargetId, ok := statusReport.TargetId.(*pb.StatusReport_ReleaseId); ok {
			if releaseTargetId.ReleaseId == releaseId {
				return statusReport, nil
			}
		}
	}

	return nil, status.Errorf(codes.NotFound, "Failed to find associated Status Report by release id %q", releaseId)
}

func (c *StatusCommand) Flags() *flag.Sets {
	return c.flagSet(0, func(set *flag.Sets) {
		f := set.NewSet("Command Options")

		f.BoolVar(&flag.BoolVar{
			Name:    "verbose",
			Aliases: []string{"V"},
			Target:  &c.flagVerbose,
			Usage:   "Display more details.",
		})

		f.BoolVar(&flag.BoolVar{
			Name:   "json",
			Target: &c.flagJson,
			Usage:  "Output the status information as JSON.",
		})

		f.BoolVar(&flag.BoolVar{
			Name:   "all-projects",
			Target: &c.flagAllProjects,
			Usage:  "Output status about every project in a workspace.",
		})
	})
}

func (c *StatusCommand) AutocompleteArgs() complete.Predictor {
	return complete.PredictNothing
}

func (c *StatusCommand) AutocompleteFlags() complete.Flags {
	return c.Flags().Completions()
}

func (c *StatusCommand) Synopsis() string {
	return "List statuses."
}

func (c *StatusCommand) Help() string {
	return formatHelp(`
Usage: waypoint status [options] [project]

  View the current status of projects and applications managed by Waypoint.

` + c.Flags().Help())
}

var (
	// Success or info messages

	wpStatusSuccessMsg = strings.TrimSpace(`
The projects listed above represent their current state known
in the Waypoint server. For more information about a project’s applications and
their current state, run ‘waypoint status PROJECT-NAME’.
`)

	wpStatusProjectSuccessMsg = strings.TrimSpace(`
The project and its apps listed above represents its current state known
in the Waypoint server. For more information about a project’s applications and
their current state, run ‘waypoint status -app=APP-NAME PROJECT-NAME’.
`)

	wpStatusAppSuccessMsg = strings.TrimSpace(`
The application and its declared resources listed above represents its current state known
in the Waypoint server.
`)

	wpStatusMsg = "Current project statuses in server context %q"

	wpStatusProjectMsg = "Current status for project %q in server context %q."

	wpStatusAppProjectMsg = strings.TrimSpace(`
Current status for application % q in project %q in server context %q.
`)

	// Failure messages

	wpStatusHealthTriageMsg = strings.TrimSpace(`
To see more information about the failing application, please check out the application logs:

waypoint logs -app=APP-NAME

The projects listed above represent their current state known
in Waypoint server. For more information about an application defined in the
project %[1]q can be viewed by running the command:

waypoint status -app=APP-NAME %[1]s
`)

	wpProjectNotFound = strings.TrimSpace(`
No project name %q was found for the server context %q. To see a list of
currently configured projects, run “waypoint project list”.

If you want more information for a specific application, use the '-app' flag
with “waypoint status -app=APP-NAME PROJECT-NAME”.
`)

	wpAppFlagAndTargetIncludedMsg = strings.TrimSpace(`
The 'app' flag was included, but an application was also requested as an argument.
The app flag will be ignored.
`)

	// TODO do we need a "waypoint application list"
	wpAppNotFound = strings.TrimSpace(`
No application named %q was found in project %q for the server context %q. To see a
list of currently configured projects, run “waypoint project list”.
`)
)
