// Public surface of the generated API contract. Consumers import types from
// here so frontend code never hand-writes shapes that belong to the spec.
export type paths = import("./schema").paths;
export type components = import("./schema").components;
export type webhooks = import("./schema").webhooks;
export type $defs = import("./schema").$defs;

export type operations = import("./schema").operations;

// Convenience aliases for the entities clients consume most.
export type Tenant = components["schemas"]["Tenant"];
export type Project = components["schemas"]["Project"];
export type Thread = components["schemas"]["Thread"];
export type Message = components["schemas"]["Message"];
export type Run = components["schemas"]["Run"];
export type Task = components["schemas"]["Task"];
export type RunReport = components["schemas"]["RunReport"];
export type Event = components["schemas"]["Event"];
export type Problem = components["schemas"]["Problem"];
