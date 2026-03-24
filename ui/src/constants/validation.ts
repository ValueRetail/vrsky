// Validation constants for workspace and tenant management
export const WORKSPACE_NAME_MIN_LENGTH = 3;
export const WORKSPACE_NAME_MAX_LENGTH = 255;

// Error messages
export const VALIDATION_MESSAGES = {
  WORKSPACE_NAME_TOO_SHORT: `Workspace name must be at least ${WORKSPACE_NAME_MIN_LENGTH} characters`,
  WORKSPACE_NAME_TOO_LONG: `Workspace name must not exceed ${WORKSPACE_NAME_MAX_LENGTH} characters`,
  WORKSPACE_NAME_REQUIRED: 'Workspace name is required',
} as const;
