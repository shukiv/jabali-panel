<?php

declare(strict_types=1);

namespace App\Providers\Filament;

use App\Filament\AvatarProviders\InitialsAvatarProvider;
use App\Filament\Jabali\Pages\Auth\Login;
use App\Http\Middleware\RedirectAdminFromUserPanel;
use App\Http\Middleware\SetLocale;
use App\Models\DnsSetting;
use Filament\Http\Middleware\Authenticate;
use Filament\Http\Middleware\AuthenticateSession;
use Filament\Http\Middleware\DisableBladeIconComponents;
use Filament\Http\Middleware\DispatchServingFilamentEvent;
use Filament\Panel;
use Filament\PanelProvider;
use Filament\Support\Colors\Color;
use Filament\View\PanelsRenderHook;
use Filament\Widgets\AccountWidget;
use Illuminate\Cookie\Middleware\AddQueuedCookiesToResponse;
use Illuminate\Cookie\Middleware\EncryptCookies;
use Illuminate\Foundation\Http\Middleware\VerifyCsrfToken;
use Illuminate\Routing\Middleware\SubstituteBindings;
use Illuminate\Session\Middleware\StartSession;
use Illuminate\View\Middleware\ShareErrorsFromSession;

class JabaliPanelProvider extends PanelProvider
{
    public function panel(Panel $panel): Panel
    {
        return $panel
            ->default()
            ->id('jabali')
            ->path('jabali-panel')
            ->login(Login::class)
            // ->registration()
            ->passwordReset()
            ->profile()
            ->defaultAvatarProvider(InitialsAvatarProvider::class)
            ->colors([
                'primary' => Color::Blue,
            ])
            ->darkMode()
            ->brandName(fn () => DnsSetting::get('panel_name', 'Jabali'))
            ->brandLogo(fn () => ($logo = DnsSetting::get('custom_logo')) ? asset('storage/'.$logo) : asset('images/jabali_logo.svg'))
            ->darkModeBrandLogo(fn () => ($logo = DnsSetting::get('custom_logo_dark', DnsSetting::get('custom_logo'))) ? asset('storage/'.$logo) : asset('images/jabali_logo_dark.svg'))
            ->brandLogoHeight('2rem')
            ->favicon(asset('favicon.ico'))
            ->renderHook(
                PanelsRenderHook::HEAD_END,
                fn () => $this->getOpenGraphTags('Jabali Panel', 'Web hosting control panel - Manage your domains, emails, databases and more').
                    \Illuminate\Support\Facades\Vite::useBuildDirectory('build')->withEntryPoints(['resources/css/app.css', 'resources/js/server-charts.js'])->toHtml().
$this->getRtlScript()
            )
            ->renderHook(
                PanelsRenderHook::BODY_START,
                fn () => (request()->routeIs('filament.jabali.auth.login') ? $this->getLoginWordCloud() : '').$this->renderImpersonationNotice()
            )
            ->renderHook(
                PanelsRenderHook::SCRIPTS_AFTER,
                fn () => view('components.tab-skeleton-script').
                    \Illuminate\Support\Facades\Vite::useBuildDirectory('build')->withEntryPoints(['resources/js/server-charts.js'])->toHtml(),
            )
            ->renderHook(
                PanelsRenderHook::FOOTER,
                fn () => view('vendor.filament-panels.components.footer')
            )
            ->renderHook(
                PanelsRenderHook::USER_MENU_BEFORE,
                fn () => view('components.language-switcher')
            )
            ->discoverResources(in: app_path('Filament/Jabali/Resources'), for: 'App\\Filament\\Jabali\\Resources')
            ->discoverPages(in: app_path('Filament/Jabali/Pages'), for: 'App\\Filament\\Jabali\\Pages')
            ->pages([])
            ->discoverWidgets(in: app_path('Filament/Jabali/Widgets'), for: 'App\\Filament\\Jabali\\Widgets')
            ->widgets([
                AccountWidget::class,
            ])
            ->middleware([
                EncryptCookies::class,
                AddQueuedCookiesToResponse::class,
                StartSession::class,
                AuthenticateSession::class,
                ShareErrorsFromSession::class,
                VerifyCsrfToken::class,
                SubstituteBindings::class,
                DisableBladeIconComponents::class,
                DispatchServingFilamentEvent::class,
                SetLocale::class,
            ])
            ->authMiddleware([
                Authenticate::class,
                RedirectAdminFromUserPanel::class,
            ])
            ->plugins(array_filter([
                class_exists(\App\FileBrowser\FileBrowserPlugin::class)
                    ? \App\FileBrowser\FileBrowserPlugin::make()->slug('files')
                    : null,
            ]));
    }

    protected function getRtlScript(): string
    {
        $locale = app()->getLocale();
        $direction = config("languages.supported.{$locale}.direction", 'ltr');

        if ($direction === 'rtl') {
            return '<script>document.documentElement.setAttribute("dir", "rtl");</script><style>.fi-topbar-item-label{direction:ltr;}</style>';
        }

        return '';
    }

    protected function renderImpersonationNotice(): string
    {
        if (! session()->has('impersonated_by')) {
            return '';
        }

        return view('components.impersonation-banner')->render();
    }

    protected function getOpenGraphTags(string $title, string $description): string
    {
        $url = e(url()->current());
        $image = e(asset('images/og-image.png'));
        $siteName = e(DnsSetting::get('panel_name', 'Jabali'));
        $title = e($title);
        $description = e($description);

        return <<<HTML
        <meta property="og:type" content="website">
        <meta property="og:title" content="{$title}">
        <meta property="og:description" content="{$description}">
        <meta property="og:url" content="{$url}">
        <meta property="og:image" content="{$image}">
        <meta property="og:site_name" content="{$siteName}">
        <meta name="twitter:card" content="summary_large_image">
        <meta name="twitter:title" content="{$title}">
        <meta name="twitter:description" content="{$description}">
        <meta name="twitter:image" content="{$image}">
        HTML;
    }

    protected function getLoginWordCloud(): string
    {
        // "Client Dashboard" in panel supported languages (using lang/*.json Dashboard translations)
        $words = [
            'Client Dashboard',           // English
            'Panel de Cliente',           // Spanish
            'Painel de Controle Cliente', // Portuguese
            'Tableau de bord Client',     // French
            'Панель управления Клиента',  // Russian
            'לוח בקרה לקוח',              // Hebrew
            'لوحة تحكم العميل',           // Arabic
        ];

        // Generate rows with randomized words for varied pattern
        $rows = '';
        for ($row = 0; $row < 50; $row++) {
            $rowContent = '';
            $shuffled = $words;
            shuffle($shuffled);
            for ($col = 0; $col < 20; $col++) {
                $word = $shuffled[$col % count($shuffled)];
                if ($col % 3 === 0) {
                    shuffle($shuffled);
                }
                $rowContent .= $word.' · ';
            }
            $rows .= "<div class=\"pattern-row\">{$rowContent}</div>";
        }

        return <<<HTML
        <style>
            .word-pattern-container {
                position: fixed;
                top: 50%;
                left: 50%;
                width: 300vw;
                height: 300vh;
                overflow: visible;
                pointer-events: none;
                z-index: -1;
                transform: translate(-50%, -50%) rotate(-25deg);
                display: flex;
                flex-direction: column;
                justify-content: center;
                align-items: center;
                opacity: 0.08;
            }
            .pattern-row {
                white-space: nowrap;
                font-size: 16px;
                font-weight: 700;
                color: #2563eb;
                font-family: "Segoe UI", Arial, "Noto Sans", "Noto Sans Arabic", "Noto Sans Hebrew", sans-serif;
                text-transform: uppercase;
                letter-spacing: 0.08em;
                line-height: 1.5;
            }
            .pattern-row:nth-child(even) {
                margin-left: 120px;
            }
            .fi-simple-layout {
                position: relative;
                z-index: 1;
            }
            .fi-simple-main {
                position: relative;
                z-index: 10;
            }
            .fi-simple-main-ctn {
                position: relative;
                z-index: 10;
            }
        </style>
        <div class="word-pattern-container">{$rows}</div>
        HTML;
    }
}
