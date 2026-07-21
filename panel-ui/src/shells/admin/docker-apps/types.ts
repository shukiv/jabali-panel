// Types for the M48 docker-app marketplace UI. Mirrors the JSON
// shapes panel-api/internal/api/docker_apps.go serialises.

export interface CatalogPortSpec {
  name: string;
  container_port: number;
  protocol: "tcp" | "udp";
  default_enabled: boolean;
  default_bind: "loopback" | "public";
  default_reverse_proxy: boolean;
  health_path?: string;
}

export interface CatalogVolume {
  name: string;
  container_path: string;
}

export interface CatalogResources {
  cpu?: string;
  memory?: string;
  pids?: number;
}

export interface CatalogEnvVar {
  name: string;
  value?: string;
  secret?: boolean;
  generate?: "password32" | "password64";
}

export interface CatalogEntry {
  slug: string;
  name: string;
  version: string;
  description: string;
  tags?: string[];
  icon: string;
  upstream?: string;
  documentation?: string;
  update_mode: "manual" | "auto";
  resources: CatalogResources;
  volumes: CatalogVolume[];
  ports: CatalogPortSpec[];
  env?: CatalogEnvVar[];
}

export interface InstalledPort {
  id: string;
  app_id: string;
  port_name: string;
  container_port: number;
  bind_interface: string;
  host_port: number;
  protocol: "tcp" | "udp";
  reverse_proxy: boolean;
  enabled: boolean;
  created_at: string;
}

export interface InstalledApp {
  id: string;
  user_id?: string | null;
  slug: string;
  name: string;
  catalog_version: string;
  image_sha: string | null;
  available_digest: string | null;
  last_check_at: string | null;
  backup_destination_id: string | null;
  status:
    | "pending"
    | "installing"
    | "running"
    | "stopped"
    | "failed"
    | "updating"
    | "rolling_back"
    | "deleted";
  update_mode: "manual" | "auto";
  cpu_limit: string | null;
  memory_limit: string | null;
  pids_limit: number | null;
  data_bytes: number;
  size_checked_at: string | null;
  last_error: string | null;
  created_at: string;
  updated_at: string;
  domain?: string;
  // First-run guidance from the catalog entry (GH #521), with {{domain}}
  // already substituted server-side. Shown as a "Setup" hint on the card so
  // operators know the first-run step (e.g. Pocket ID: create the admin at
  // https://<domain>/setup). Absent when the catalog entry has no note.
  post_install_note?: string;
  ports: InstalledPort[];
}

export interface InstallRequest {
  slug: string;
  name: string;
  domain?: string;
  update_mode?: "manual" | "auto";
  cpu_limit?: string;
  memory_limit?: string;
  pids_limit?: number;
  env?: Record<string, string>;
  ports?: InstallPortOverride[];
  backup_destination_id?: string;
}

export interface InstallPortOverride {
  name: string;
  enabled?: boolean;
  bind_interface?: string;
  host_port?: number;
  reverse_proxy?: boolean;
}
