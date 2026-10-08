import createClient from "openapi-fetch";
import type { components, paths } from "./schema";

export type WorkflowRun = components["schemas"]["WorkflowRun"];
export type Workflow = components["schemas"]["Workflow"];
export type StepView = components["schemas"]["StepView"];
export type Connection = components["schemas"]["Connection"];
export type TimelineEvent = components["schemas"]["TimelineEvent"];
export type StartRequest = components["schemas"]["StartRequest"];

export const API_BASE = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

export const api = createClient<paths>({ baseUrl: API_BASE });
