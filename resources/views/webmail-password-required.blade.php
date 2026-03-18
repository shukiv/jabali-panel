<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{ ($has_password_hash ?? false) ? __('Webmail Login Required') : __('Password Reset Required') }}</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            margin: 0;
            padding: 20px;
        }
        .card {
            background: white;
            border-radius: 12px;
            box-shadow: 0 10px 40px rgba(0,0,0,0.2);
            padding: 40px;
            max-width: 450px;
            text-align: center;
        }
        .icon {
            width: 64px;
            height: 64px;
            margin: 0 auto 20px;
            background: #fef3c7;
            border-radius: 50%;
            display: flex;
            align-items: center;
            justify-content: center;
        }
        .icon svg {
            width: 32px;
            height: 32px;
            color: #d97706;
        }
        h1 {
            color: #1f2937;
            font-size: 24px;
            margin: 0 0 12px;
        }
        .email {
            color: #6b7280;
            font-size: 14px;
            margin-bottom: 20px;
        }
        p {
            color: #4b5563;
            line-height: 1.6;
            margin: 0 0 24px;
        }
        .btn {
            display: inline-block;
            background: #667eea;
            color: white;
            padding: 12px 24px;
            border-radius: 8px;
            text-decoration: none;
            font-weight: 500;
            transition: background 0.2s;
        }
        .btn:hover {
            background: #5a67d8;
        }
        .hint {
            margin-top: 20px;
            padding-top: 20px;
            border-top: 1px solid #e5e7eb;
            font-size: 13px;
            color: #9ca3af;
        }
    </style>
</head>
<body>
    <div class="card">
        <div class="icon">
            <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 10-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 002.25-2.25v-6.75a2.25 2.25 0 00-2.25-2.25H6.75a2.25 2.25 0 00-2.25 2.25v6.75a2.25 2.25 0 002.25 2.25z" />
            </svg>
        </div>
        <h1>{{ ($has_password_hash ?? false) ? __('Webmail Login Required') : __('Password Reset Required') }}</h1>
        <div class="email">{{ $email }}</div>
        @if (($has_password_hash ?? false))
            <p>{{ __('This mailbox was migrated, so one-click webmail login is unavailable. You can sign in directly using your existing mailbox password.') }}</p>
            <a href="{{ url('/webmail') }}" class="btn">
                {{ config('jabali.mail_backend') === 'stalwart' ? __('Open Bulwark Webmail') : __('Open Webmail Login') }}
            </a>
            <div class="hint">
                {{ __('If you want one-click webmail access, reset the mailbox password in Email settings.') }}
            </div>
        @else
            <p>{{ __('To access webmail, you need to set a new password for this mailbox. Click the button below to go to Email settings and reset your password.') }}</p>
            <a href="{{ url('/jabali-panel/email') }}" class="btn">{{ __('Go to Email Settings') }}</a>
            <div class="hint">
                {{ __('After setting your password, you can use the Webmail button to access your inbox.') }}
            </div>
        @endif
    </div>
</body>
</html>
