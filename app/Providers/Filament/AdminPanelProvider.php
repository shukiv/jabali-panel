<?php

namespace App\Providers\Filament;

use App\Filament\Admin\Pages\Auth\Login as AdminLogin;
use App\Filament\Admin\Pages\Dashboard;
use App\Filament\AvatarProviders\InitialsAvatarProvider;
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

class AdminPanelProvider extends PanelProvider
{
    public function panel(Panel $panel): Panel
    {
        return $panel
            ->id('admin')
            ->path('jabali-admin')
            ->viteTheme('resources/css/filament/admin/theme.css')
            ->authGuard('admin')
            ->login(AdminLogin::class)
            ->passwordReset()
            ->defaultAvatarProvider(InitialsAvatarProvider::class)
            ->colors([
                'primary' => Color::Red,
            ])
            ->darkMode()
            ->brandName(fn () => $this->getAdminBrandName())
            ->brandLogo(fn () => ($logo = DnsSetting::get('custom_logo')) ? asset('storage/'.$logo) : asset('images/jabali_logo.svg'))
            ->darkModeBrandLogo(fn () => ($logo = DnsSetting::get('custom_logo')) ? asset('storage/'.$logo) : asset('images/jabali_logo_dark.svg'))
            ->brandLogoHeight('2rem')
            ->favicon(asset('favicon.ico'))
            ->renderHook(
                PanelsRenderHook::HEAD_END,
                fn () => $this->getOpenGraphTags($this->getAdminBrandName(), 'Server administration panel for Jabali - Manage your hosting infrastructure').
                    \Illuminate\Support\Facades\Vite::useBuildDirectory('build')->withEntryPoints(['resources/css/app.css'])->toHtml().
$this->getRtlScript()
            )
            ->renderHook(
                PanelsRenderHook::BODY_START,
                fn () => request()->routeIs('filament.admin.auth.login') ? $this->getLoginWordCloud() : ''
            )
            ->renderHook(
                PanelsRenderHook::FOOTER,
                fn () => view('vendor.filament-panels.components.footer')
            )
            ->renderHook(
                PanelsRenderHook::USER_MENU_BEFORE,
                fn () => view('components.language-switcher')
            )
            ->discoverResources(in: app_path('Filament/Admin/Resources'), for: 'App\\Filament\\Admin\\Resources')
            ->discoverPages(in: app_path('Filament/Admin/Pages'), for: 'App\\Filament\\Admin\\Pages')
            ->pages([
                Dashboard::class,
            ])
            ->discoverWidgets(in: app_path('Filament/Admin/Widgets'), for: 'App\\Filament\\Admin\\Widgets')
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
            ]);
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

    protected function getAdminBrandName(): string
    {
        $brandName = DnsSetting::get('panel_name', 'Jabali');
        $cleaned = trim((string) preg_replace('/\\s*Admin\\s*$/i', '', $brandName));

        return $cleaned !== '' ? $cleaned : 'Jabali';
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
        // "Administrator" in panel supported languages (from lang/*.json)
        $words = [
            'Administrator',   // English
            'Administrador',   // Spanish & Portuguese
            'Administrateur',  // French
            'Администратор',   // Russian
            'מנהל',            // Hebrew
            'مشرف',            // Arabic
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
                } // Re-shuffle periodically
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
                color: #dc2626;
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
