// AntD-compatible icon shims backed by lucide-react.
//
// Keep the AntD icon names at every call-site. Each export renders the
// matching lucide component sized at 1em × 1em so the existing
// `style={{ fontSize: N }}` pattern (inherited by `color: currentColor`)
// keeps working, matching how AntD icons consumed size.
//
// AntD internals (Dropdown caret, Table sort arrows, Modal close) still
// resolve @ant-design/icons directly — we only swap application-level
// icons.
import type { CSSProperties, MouseEventHandler } from "react";
import {
  AlertCircle,
  AlertTriangle,
  ArrowLeft,
  ArrowLeftRight,
  Book,
  Bug,
  CalendarCheck,
  ChartBar,
  Check,
  CheckCircle2,
  CircleX,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  CircleAlert,
  CircleHelp,
  Clock,
  Code,
  FilePlus,
  Import,
  SquareTerminal,
  Copy,
  Database,
  Download,
  EthernetPort,
  ExternalLink,
  Eye,
  EyeOff,
  File,
  FileArchive,
  FileCode2,
  FileImage,
  FileMinus,
  FilePlay,
  FileVolume,
  FileText,
  Info,
  MonitorPlay,
  FlaskConical,
  Folder,
  FolderOpen,
  FolderPlus,
  Globe,
  Grip,
  Container,
  HardDrive,
  HardDriveDownload,
  HardDriveUpload,
  Home,
  Inbox,
  Bell,
  Key,
  LayoutGrid,
  LayoutPanelLeft,
  LifeBuoy,
  Link as LinkIcon,
  Loader2,
  Lock,
  LogIn,
  LogOut,
  Mail,
  Menu,
  Moon,
  MoreHorizontal,
  Move,
  PackageOpen,
  Palette,
  PauseCircle,
  Pencil,
  PlayCircle,
  Plug,
  Plus,
  Power,
  RefreshCw,
  Redo2,
  RotateCcw,
  RotateCw,
  Save,
  Search,
  Send,
  Server,
  Settings,
  ShieldCheck,
  SquarePlus,
  Sun,
  Trash2,
  Upload,
  User,
  Users,
  UserPlus,
  Waypoints,
  Wrench,
  X,
  Zap,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

export type IconProps = {
  className?: string;
  style?: CSSProperties;
  onClick?: MouseEventHandler<SVGSVGElement>;
  spin?: boolean;
  rotate?: number;
  title?: string;
  twoToneColor?: string;
};

const rotationStyle = (rotate?: number): CSSProperties | undefined =>
  rotate ? { transform: `rotate(${rotate}deg)` } : undefined;

const spinStyle: CSSProperties = {
  animation: "jabali-icon-spin 1s linear infinite",
};

// Inject the @keyframes once for the spin animation. Global so all icon
// instances share it; cheap compared to re-declaring per render.
if (typeof document !== "undefined" && !document.getElementById("jabali-icon-spin-style")) {
  const s = document.createElement("style");
  s.id = "jabali-icon-spin-style";
  s.textContent =
    "@keyframes jabali-icon-spin{from{transform:rotate(0)}to{transform:rotate(360deg)}}";
  document.head.appendChild(s);
}

const shim = (Icon: LucideIcon, defaults?: { spin?: boolean }) => {
  const Shimmed = ({ className, style, onClick, spin, rotate, title, twoToneColor }: IconProps) => {
    const mergedStyle: CSSProperties = {
      width: "1em",
      height: "1em",
      verticalAlign: "-0.125em",
      ...(twoToneColor ? { color: twoToneColor } : null),
      ...(style ?? null),
      ...(spin ?? defaults?.spin ? spinStyle : null),
      ...rotationStyle(rotate),
    };
    // Add the `anticon` class so AntD component-local CSS selectors
    // (Tag icon+text gap, Button icon+text gap, etc.) match. Without
    // it, the shimmed lucide <svg> sits flush against adjacent text.
    const mergedClass = className ? `anticon ${className}` : "anticon";
    return (
      <Icon
        className={mergedClass}
        style={mergedStyle}
        onClick={onClick as never}
        strokeWidth={1.5}
        aria-label={title}
      />
    );
  };
  Shimmed.displayName = (Icon.displayName ?? Icon.name ?? "Icon") + "Outlined";
  return Shimmed;
};

// --- Nav / core ---
export const HomeOutlined = shim(Home);
export const GlobalOutlined = shim(Globe);
export const EthernetPortOutlined = shim(EthernetPort);
export const LockOutlined = shim(Lock);
export const CodeOutlined = shim(Code);
export const SquareTerminalOutlined = shim(SquareTerminal);
export const DatabaseOutlined = shim(Database);
export const HddOutlined = shim(HardDrive);
export const HardDriveDownloadOutlined = shim(HardDriveDownload);
export const HardDriveUploadOutlined = shim(HardDriveUpload);
export const BgColorsOutlined = shim(Palette);
export const FolderOutlined = shim(Folder);
export const FolderOpenOutlined = shim(FolderOpen);
export const FolderAddOutlined = shim(FolderPlus);
export const AppstoreOutlined = shim(LayoutGrid);
export const LayoutPanelLeftOutlined = shim(LayoutPanelLeft);
export const ContainerOutlined = shim(Container);
export const PackageOpenOutlined = shim(PackageOpen);
export const AppstoreAddOutlined = shim(SquarePlus);
export const KeyOutlined = shim(Key);
export const ClockCircleOutlined = shim(Clock);
export const BellOutlined = shim(Bell);
export const SendOutlined = shim(Send);
export const MailOutlined = shim(Mail);
export const SettingOutlined = shim(Settings);
export const TeamOutlined = shim(Users);
export const UsergroupAddOutlined = shim(UserPlus);
export const CloudServerOutlined = shim(Waypoints);
export const ServerOutlined = shim(Server);
export const ChartBarOutlined = shim(ChartBar);
export const ThunderboltOutlined = shim(Zap);
export const FlaskConicalOutlined = shim(FlaskConical);

// --- Actions ---
export const PlusOutlined = shim(Plus);
export const PlusSquareOutlined = shim(SquarePlus);
export const DeleteOutlined = shim(Trash2);
export const EditOutlined = shim(Pencil);
export const CopyOutlined = shim(Copy);
export const DownloadOutlined = shim(Download);
export const ImportOutlined = shim(Import);
export const FileAddOutlined = shim(FilePlus);
export const UploadOutlined = shim(Upload);
export const SaveOutlined = shim(Save);
export const SearchOutlined = shim(Search);
export const ReloadOutlined = shim(RefreshCw);
export const SyncOutlined = shim(RotateCw);
export const RotateCcwOutlined = shim(RotateCcw);
export const RedoOutlined = shim(Redo2);
export const ApiOutlined = shim(Plug);
export const BookOutlined = shim(Book);
export const BugOutlined = shim(Bug);
export const LifeBuoyOutlined = shim(LifeBuoy);
export const DragOutlined = shim(Move);
export const FolderInputOutlined = shim(Move);
export const LinkOutlined = shim(LinkIcon);
export const LoginOutlined = shim(LogIn);
export const LogoutOutlined = shim(LogOut);
export const MenuOutlined = shim(Menu);
export const MoreOutlined = shim(MoreHorizontal);
export const PoweroffOutlined = shim(Power);
export const SafetyCertificateOutlined = shim(ShieldCheck);
export const SafetyOutlined = shim(ShieldCheck);
export const ShieldCheckOutlined = shim(ShieldCheck);
export const CalendarCheckOutlined = shim(CalendarCheck);
export const SwapOutlined = shim(ArrowLeftRight);
export const ToolOutlined = shim(Wrench);
export const AppstoreLayoutOutlined = shim(Grip);

// --- Status ---
export const CheckOutlined = shim(Check);
export const CheckCircleOutlined = shim(CheckCircle2);
export const CheckCircleTwoTone = shim(CheckCircle2);
export const CloseOutlined = shim(X);
export const CloseCircleOutlined = shim(CircleX);
export const ExclamationCircleOutlined = shim(CircleAlert ?? AlertCircle);
export const QuestionCircleOutlined = shim(CircleHelp);
export const WarningOutlined = shim(AlertTriangle);
export const LoadingOutlined = shim(Loader2, { spin: true });
export const InboxOutlined = shim(Inbox);
export const PauseCircleOutlined = shim(PauseCircle);
export const PlayCircleOutlined = shim(PlayCircle);

// --- Arrows ---
export const DownOutlined = shim(ChevronDown);
export const UpOutlined = shim(ChevronUp);
export const LeftOutlined = shim(ChevronLeft);
export const RightOutlined = shim(ChevronRight);
export const ArrowLeftOutlined = shim(ArrowLeft);

// --- Files ---
export const FileOutlined = shim(File);
export const FileArchiveOutlined = shim(FileArchive);
export const FileCodeOutlined = shim(FileCode2);
export const FileImageOutlined = shim(FileImage);
export const FileMinusOutlined = shim(FileMinus);
export const FilePlayOutlined = shim(FilePlay);
export const FileVolumeOutlined = shim(FileVolume);
export const MonitorPlayOutlined = shim(MonitorPlay);
export const FileTextOutlined = shim(FileText);
export const InfoCircleOutlined = shim(Info);
export const ExportOutlined = shim(ExternalLink);

// --- Misc ---
export const EyeOutlined = shim(Eye);
export const EyeInvisibleOutlined = shim(EyeOff);
export const UserOutlined = shim(User);
export const MoonOutlined = shim(Moon);
export const SunOutlined = shim(Sun);
// Lucide dropped brand icons at 0.300; inline the GitHub octocat so we
// don't reintroduce @ant-design/icons for one glyph.
export const GithubOutlined = ({ className, style, onClick, title }: IconProps) => (
  <svg
    className={className}
    style={{ width: "1em", height: "1em", verticalAlign: "-0.125em", ...(style ?? null) }}
    onClick={onClick as never}
    viewBox="0 0 24 24"
    fill="currentColor"
    aria-label={title}
  >
    <path d="M12 .5C5.73.5.64 5.59.64 11.86c0 5.02 3.26 9.28 7.78 10.78.57.1.78-.25.78-.55 0-.27-.01-.99-.02-1.94-3.17.69-3.83-1.53-3.83-1.53-.52-1.31-1.27-1.66-1.27-1.66-1.04-.71.08-.7.08-.7 1.15.08 1.75 1.18 1.75 1.18 1.02 1.75 2.68 1.24 3.33.95.1-.74.4-1.24.72-1.53-2.53-.29-5.2-1.27-5.2-5.63 0-1.24.44-2.26 1.17-3.05-.12-.29-.51-1.45.11-3.02 0 0 .96-.31 3.14 1.17.91-.25 1.89-.38 2.86-.38s1.95.13 2.86.38c2.18-1.48 3.14-1.17 3.14-1.17.62 1.57.23 2.73.11 3.02.73.79 1.17 1.81 1.17 3.05 0 4.37-2.68 5.33-5.22 5.61.41.35.77 1.04.77 2.1 0 1.52-.01 2.74-.01 3.12 0 .3.21.66.79.55 4.52-1.5 7.77-5.76 7.77-10.78C23.36 5.59 18.27.5 12 .5z" />
  </svg>
);
