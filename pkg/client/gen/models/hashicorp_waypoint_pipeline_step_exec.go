// Code generated by go-swagger; DO NOT EDIT.

package models

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"context"

	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag"
)

// HashicorpWaypointPipelineStepExec hashicorp waypoint pipeline step exec
//
// swagger:model hashicorp.waypoint.Pipeline.Step.Exec
type HashicorpWaypointPipelineStepExec struct {

	// args
	Args []string `json:"args"`

	// Command to execute within the image. If blank, the default command
	// will be executed.
	Command string `json:"command,omitempty"`

	// TODO(briancain): update this to use the Step exec instead of the plugin
	// Docker image to execute. This should be a fully qualified image URL.
	Image string `json:"image,omitempty"`
}

// Validate validates this hashicorp waypoint pipeline step exec
func (m *HashicorpWaypointPipelineStepExec) Validate(formats strfmt.Registry) error {
	return nil
}

// ContextValidate validates this hashicorp waypoint pipeline step exec based on context it is used
func (m *HashicorpWaypointPipelineStepExec) ContextValidate(ctx context.Context, formats strfmt.Registry) error {
	return nil
}

// MarshalBinary interface implementation
func (m *HashicorpWaypointPipelineStepExec) MarshalBinary() ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return swag.WriteJSON(m)
}

// UnmarshalBinary interface implementation
func (m *HashicorpWaypointPipelineStepExec) UnmarshalBinary(b []byte) error {
	var res HashicorpWaypointPipelineStepExec
	if err := swag.ReadJSON(b, &res); err != nil {
		return err
	}
	*m = res
	return nil
}