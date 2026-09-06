import { useTranslation } from "react-i18next";
import { Tabs, Alert, Card, Select, Space, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useEffect, useState } from "react";
import { CodeOutlined } from "@icons";
import { UserPHPPerformanceCard } from "./UserPHPPerformanceCard";
import { UserPHPOpcacheCard } from "./UserPHPOpcacheCard";
import { DomainEnvVarsCard } from "./DomainEnvVarsCard";
import { PHPExtensionsCard } from "./PHPExtensionsCard";
import { PHPXdebugCard } from "./PHPXdebugCard";
import { apiClient } from "../../../apiClient";
import { getIdentity, type Identity } from "../../../identity";
import { DomainPHPSettingsPanel } from "../../../components/domains/DomainPHPSettingsPanel";

type Domain = {
  id: string;
  name: string;
  user_id: string;
};

export function UserPHPSettingsPage() {
  const { t } = useTranslation();
  const [, setMe] = useState<Identity | null>(null);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [selectedDomain, setSelectedDomain] = useState<string | null>(null);
  const [availableVersions, setAvailableVersions] = useState<string[]>([]);
  const [cliVersion, setCliVersion] = useState<string>(""); // "" = auto
  const [cliSaving, setCliSaving] = useState(false);
  const [composerChannel, setComposerChannel] = useState<string>("latest");
  const [composerSaving, setComposerSaving] = useState(false);

  // Load identity, domains, and installed PHP versions on mount
  useEffect(() => {
    (async () => {
      const identity = await getIdentity();
      setMe(identity);

      try {
        const resp = await apiClient.get<{ data: Domain[]; total: number }>(
          "/domains",
        );
        setDomains(resp.data?.data ?? []);
      } catch {
        feedback.message.error("Failed to load domains");
      }

      try {
        const resp = await apiClient.get<{ versions: string[] }>(
          "/php/versions",
        );
        setAvailableVersions(resp.data?.versions ?? []);
      } catch {
        // Non-fatal: CLI version selector falls back to "Automatic" only.
      }

      try {
        const resp = await apiClient.get<{ version: string }>(
          "/me/php-cli-version",
        );
        setCliVersion(resp.data?.version ?? "");
      } catch {
        // Non-fatal: account may have no shell user.
      }

      try {
        const resp = await apiClient.get<{ channel: string }>(
          "/me/composer-channel",
        );
        setComposerChannel(resp.data?.channel ?? "latest");
      } catch {
        // Non-fatal: account may have no shell user.
      }
    })();
  }, []);

  const onChangeComposer = async (channel: string) => {
    setComposerSaving(true);
    try {
      await apiClient.put("/me/composer-channel", { channel });
      setComposerChannel(channel);
      feedback.message.success(
        channel === "lts"
          ? "Composer set to the 2.2 LTS channel"
          : "Composer set to the latest channel",
      );
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to set Composer version",
      );
    } finally {
      setComposerSaving(false);
    }
  };

  const onChangeCliVersion = async (version: string) => {
    setCliSaving(true);
    try {
      await apiClient.put("/me/php-cli-version", { version });
      setCliVersion(version);
      feedback.message.success(
        version
          ? `CLI default set to PHP ${version}`
          : "CLI default reverted to automatic",
      );
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to set CLI PHP version",
      );
    } finally {
      setCliSaving(false);
    }
  };

  return (
    // GH #1332 item 1: the page was clamped to 800px and centred, unlike every
    // other settings page. Use the shell's full responsive width like its peers.
    <div>
      <Space direction="vertical" size="large" style={{ width: "100%" }}>
        <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
          <CodeOutlined /> PHP Settings
        </Typography.Title>

        <Alert
          title={t("userphpsettingspage.caution")}
          description={t("userphpsettingspage.changing_php_settings_can_affect_your_websit")}
          type="warning"
          showIcon
        />

        <Tabs
          defaultActiveKey="settings"
          items={[
            {
              key: "settings",
              label: "Version & Domains",
              children: (
                <Space direction="vertical" size="large" style={{ width: "100%" }}>
                  <Card title={t("userphpsettingspage.cli_terminal_default_php_version")} size="small">
                    <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
                      Sets which PHP version a bare <code>php</code> (and composer / wp-cli)
                      uses in your SSH/terminal sessions. This is separate from each
                      domain&apos;s web PHP version. You can always pick a specific version
                      per command with <code>php8.3</code>, <code>php8.4</code>, etc.
                    </Typography.Paragraph>
                    <Select
                      style={{ minWidth: 280 }}
                      value={cliVersion}
                      loading={cliSaving}
                      onChange={onChangeCliVersion}
                      options={[
                        { value: "", label: "Automatic (follow domain pool)" },
                        ...availableVersions.map((v) => ({ value: v, label: `PHP ${v}` })),
                      ]}
                    />
                    {/* GH #1332 item 13: Composer version channel. */}
                    <Typography.Paragraph
                      type="secondary"
                      style={{ marginTop: 16, marginBottom: 4 }}
                    >
                      Composer version for your shell <code>composer</code>.
                    </Typography.Paragraph>
                    <Select
                      style={{ minWidth: 280 }}
                      value={composerChannel}
                      loading={composerSaving}
                      onChange={onChangeComposer}
                      options={[
                        { value: "latest", label: "Composer (latest)" },
                        { value: "lts", label: "Composer 2.2 LTS (older PHP compatibility)" },
                      ]}
                    />
                  </Card>

                  {/* GH #1543: the per-domain PHP controls (version + php.ini
                      limit overrides) live in DomainPHPSettingsPanel, shared
                      with the Web Domain page's PHP Settings tab. Here a picker
                      selects the domain; on the domain page the domain is fixed
                      by the route. */}
                  <Card>
                    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
                      <Select
                        style={{ minWidth: 280 }}
                        placeholder={t("userphpsettingspage.select_a_domain")}
                        value={selectedDomain}
                        onChange={setSelectedDomain}
                        options={domains.map((d) => ({ label: d.name, value: d.id }))}
                      />
                      {selectedDomain && (
                        <DomainPHPSettingsPanel key={selectedDomain} domainId={selectedDomain} />
                      )}
                    </Space>
                  </Card>
                </Space>
              ),
            },
            {
              key: "perf",
              label: "Performance",
              children: <UserPHPPerformanceCard />,
            },
            {
              key: "opcache",
              label: "OPcache & JIT",
              children: <UserPHPOpcacheCard />,
            },
            {
              key: "env",
              label: "Environment",
              children: <DomainEnvVarsCard />,
            },
            {
              key: "extensions",
              label: "Extensions",
              children: <PHPExtensionsCard />,
            },
            {
              key: "xdebug",
              label: "Xdebug",
              children: <PHPXdebugCard />,
            },
          ]}
        />
      </Space>
    </div>
  );
}
