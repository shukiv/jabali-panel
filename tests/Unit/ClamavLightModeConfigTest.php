<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class ClamavLightModeConfigTest extends TestCase
{
    public function test_light_mode_includes_sanesecurity_custom_databases(): void
    {
        $script = file_get_contents(base_path('bin/jabali-agent'));

        $this->assertIsString($script);

        $expected = [
            'DatabaseCustomURL http://ftp.swin.edu.au/sanesecurity/phish.ndb',
            'DatabaseCustomURL http://ftp.swin.edu.au/sanesecurity/sigwhitelist.ign2',
            'DatabaseCustomURL http://ftp.swin.edu.au/sanesecurity/foxhole_js.ndb',
            'DatabaseCustomURL http://ftp.swin.edu.au/sanesecurity/winnow_malware.hdb',
            'DatabaseCustomURL http://ftp.swin.edu.au/sanesecurity/winnow.attachments.hdb',
            'DatabaseCustomURL http://ftp.swin.edu.au/sanesecurity/porcupine.hsb',
        ];

        foreach ($expected as $line) {
            $this->assertStringContainsString($line, $script);
        }
    }
}
