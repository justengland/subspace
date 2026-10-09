import createClient from "openapi-fetch";
import type { components, paths } from "./schema";

export type WorkflowRun = components["schemas"]["WorkflowRun"];
export type Workflow = components["schemas"]["Workflow"];
export type Repo = components["schemas"]["Repo"];
export type StepView = components["schemas"]["StepView"];
export type ProcessView = components["schemas"]["ProcessView"];
export type Predicate = components["schemas"]["Predicate"];
export type Connection = components["schemas"]["Connection"];
export type TimelineEvent = components["schemas"]["TimelineEvent"];
export type StartRequest = components["schemas"]["StartRequest"];
export type Visualization = components["schemas"]["Visualization"];

import { API_BASE } from "./base";

export { API_BASE };

export const api = createClient<paths>({ baseUrl: API_BASE });
