// ScannerDrawer — GH #2503. A Files resource's scanner: an FTP/SFTP login
// homed at its own drop folder (in the domain's directory, outside
// public_html). The panel moves each finished upload into the shared folder,
// where members see it in webmail Files.
import { useState } from "react";
import { Alert, Button, Descriptions, Drawer, Form, Grid, Input, InputNumber, Popconfirm, Skeleton, Space, Typography } from "antd";
import { useQuery } from "@tanstack/react-query";

import { extractApiError } from "../../../../apiErrors";
import { CollapsibleAlert } from "../../../../components/CollapsibleAlert";
import { PasswordInput } from "../../../../components/PasswordInput";
import { StandardDrawerFooter } from "../../../../components/StandardActionFooter";
import { Timestamp } from "../../../../components/Timestamp";
import { getIdentity } from "../../../../identity";
import { useServerCapabilities } from "../../../../hooks/useServerCapabilities";
import {
  useDisableScanner,
  useEnableScanner,
  useScanner,
  type ScannerState,
  type SharedResource,
} from "../../../../hooks/useSharedResources";
import { feedback } from "../../../../lib/feedback"; // GH #970: themed toasts

const FORM_ID = "shared-resource-scanner-form";

interface EnableValues {
  label: string;
  password: string;
  max_mb: number;
}

export function ScannerDrawer({ resource, onClose }: { resource: SharedResource; onClose: () => void }) {
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? true;
  const { data: state, isLoading, error } = useScanner(resource.id);
  const name = resource.display_name || resource.email || resource.id;

  // The password is shown once, right after the scanner is turned on.
  const [newPassword, setNewPassword] = useState<string | null>(null);
  const enable = useEnableScanner();
  const disable = useDisableScanner();
  const { data: caps } = useServerCapabilities();
  const { data: me } = useQuery({ queryKey: ["identity"], queryFn: getIdentity });
  const [form] = Form.useForm<EnableValues>();

  // Mirrors the API's refusals so the form says why before a round trip.
  const noPackage = caps !== undefined && !caps.ftp_accounts_enabled;
  const unconfined = caps !== undefined && !caps.ftp_isolation_available && !caps.ftp_server_enabled;
  const blocked = noPackage || unconfined;

  const submit = async (v: EnableValues) => {
    try {
      await enable.mutateAsync({ id: resource.id, label: v.label, password: v.password, max_mb: v.max_mb });
      setNewPassword(v.password);
      feedback.message.success("Scanner on");
    } catch (err) {
      feedback.message.error(extractApiError(err, "Turning the scanner on failed"));
    }
  };

  const turnOff = async () => {
    try {
      await disable.mutateAsync({ id: resource.id });
      setNewPassword(null);
      feedback.message.success("Scanner off");
    } catch (err) {
      feedback.message.error(extractApiError(err, "Turning the scanner off failed"));
    }
  };

  const on = state?.enabled === true;
  const footer = on ? (
    <StandardDrawerFooter
      primaryText="Done"
      onPrimary={onClose}
      extra={
        <Popconfirm
          title="Turn the scanner off?"
          description="Removes the login. Scans already moved stay in the folder; files still waiting stay in the drop folder."
          okText="Turn off"
          okButtonProps={{ danger: true }}
          onConfirm={turnOff}
        >
          <Button danger loading={disable.isPending}>
            Turn off
          </Button>
        </Popconfirm>
      }
    />
  ) : (
    <StandardDrawerFooter
      primaryText="Turn on"
      formId={FORM_ID}
      primaryLoading={enable.isPending}
      primaryDisabled={blocked || isLoading || !!error}
      onCancel={onClose}
    />
  );

  return (
    <Drawer
      title={`Scanner — ${name}`}
      open
      onClose={onClose}
      width={isDesktop ? 520 : undefined}
      placement="right"
      destroyOnHidden
      footer={footer}
    >
      {isLoading ? (
        <Skeleton active paragraph={{ rows: 4 }} />
      ) : error ? (
        <Alert type="error" showIcon title={extractApiError(error, "Loading the scanner failed")} />
      ) : on && state ? (
        <ScannerOn state={state} newPassword={newPassword} ftpServerOn={caps?.ftp_server_enabled ?? false} />
      ) : (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <CollapsibleAlert
            storageKey="user.mail.shared.scanner.how"
            defaultOpen
            type="info"
            showIcon
            message="How a scanner works"
            description={
              <>
                <div>
                  The scanner gets its own FTP/SFTP login, which sees only its drop folder. About a minute
                  after a file finishes uploading, it moves into this shared folder, where members see it in
                  webmail Files.
                </div>
                <div>Grant members Read + delete so they can take scans away and make room.</div>
                <div>The login counts as one of your FTP/SFTP accounts.</div>
              </>
            }
          />
          {noPackage && (
            <Alert
              type="warning"
              showIcon
              title="Your hosting package doesn't include FTP/SFTP accounts, and a scanner needs one."
            />
          )}
          {!noPackage && unconfined && (
            <Alert
              type="warning"
              showIcon
              title="This server can't keep a scanner login to its folder yet"
              description="An administrator turns on the FTP server, or disk quota so logins can be isolated."
            />
          )}
          {!blocked && caps && !caps.ftp_isolation_available && (
            <Alert
              type="info"
              showIcon
              title="Here the scanner connects by FTPS only"
              description="Without disk quota on this server, an SFTP login could reach your whole home folder."
            />
          )}
          <Form<EnableValues>
            id={FORM_ID}
            form={form}
            layout="vertical"
            onFinish={submit}
            disabled={blocked}
            initialValues={{ label: "scanner", max_mb: 1024 }}
          >
            <Form.Item
              label="Login"
              name="label"
              rules={[
                { required: true, message: "Enter a login name" },
                {
                  pattern: /^[a-z0-9][a-z0-9_]{0,19}$/,
                  message: "Lowercase letters, digits, underscores; max 20 characters.",
                },
              ]}
            >
              <Input addonBefore={me?.username ? `${me.username}_` : undefined} placeholder="scanner" />
            </Form.Item>
            <Form.Item
              label="Password"
              name="password"
              extra="You enter this in the scanner's FTP or SFTP settings."
              rules={[
                { required: true, message: "Enter a password" },
                { min: 12, max: 128, message: "12 to 128 characters" },
              ]}
            >
              <PasswordInput generatorLength={20} autoComplete="new-password" />
            </Form.Item>
            <Form.Item
              label="Space"
              name="max_mb"
              extra="Scans the folder may hold. When it's full, new scans wait until members delete some."
              rules={[{ required: true, message: "Enter the space" }]}
            >
              <InputNumber min={1} max={1048576} precision={0} addonAfter="MB" style={{ width: "100%" }} />
            </Form.Item>
          </Form>
        </Space>
      )}
    </Drawer>
  );
}

function ScannerOn({
  state,
  newPassword,
  ftpServerOn,
}: {
  state: ScannerState;
  newPassword: string | null;
  ftpServerOn: boolean;
}) {
  const host = window.location.hostname;
  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      {newPassword && (
        <Alert
          type="success"
          showIcon
          title="Enter this password in the scanner now; it isn't shown again"
          description={
            <Typography.Text copyable code>
              {newPassword}
            </Typography.Text>
          }
        />
      )}
      {state.login_missing && (
        <Alert
          type="warning"
          showIcon
          title="The scanner's login was deleted"
          description="The scanner turns off on the next pass. Turn it on again to make a new login."
        />
      )}
      {state.last_error && (
        <Alert type="warning" showIcon title="Last pass" description={state.last_error} />
      )}
      <Descriptions
        column={1}
        size="small"
        bordered
        items={[
          {
            key: "login",
            label: "Login",
            children: state.username ? (
              <Typography.Text copyable code>
                {state.username}
              </Typography.Text>
            ) : (
              "—"
            ),
          },
          {
            key: "connect",
            label: "Connect",
            children: (
              <>
                {state.ftp_access && ftpServerOn && <div>FTPS: host {host}, port 21 (explicit TLS), passive mode</div>}
                {state.sftp_access && <div>SFTP: host {host}, port 22</div>}
                <div>Upload to the login's top folder (/).</div>
              </>
            ),
          },
          {
            key: "folder",
            label: "Drop folder",
            children: <Typography.Text code>{state.drop_path}</Typography.Text>,
          },
          { key: "space", label: "Space", children: `${state.max_mb ?? 0} MB` },
          { key: "moved", label: "Scans moved", children: state.ingested_total },
          {
            key: "last",
            label: "Last moved",
            children: state.last_ingest_at ? <Timestamp value={state.last_ingest_at} /> : "—",
          },
        ]}
      />
    </Space>
  );
}
