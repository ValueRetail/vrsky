/**
 * Shapes of the node-config sub-objects the builder and the property editor
 * read. On the wire a node config is a free-form Record<string, unknown>;
 * these name the keys the UI touches so the code does not cast to `any`.
 */
export type FileCfg = { path?: string }
export type HttpCfg = { url?: string; method?: string; auth_type?: string; oauth_grant_id?: string }
export type DbCfg = { host?: string; port?: number | string; database?: string; table?: string; query?: string }
