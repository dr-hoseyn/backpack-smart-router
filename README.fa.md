# نصب و بهره‌برداری BackPack Smart Router

روتر یک ورودی عمومی در ایران می‌گیرد و اتصال TCP یا جریان UDP جدید را به یکی از ورودی‌های خصوصی VMها می‌فرستد. تونل هر VM ترافیک را تا خروجی موجود حمل می‌کند. پس از اتصال مجدد به VM دیگر، IP عمومی خروجی ممکن است تغییر کند.

## نصب روی Ubuntu

### نصب تک‌خطی

```sh
curl -fsSL https://raw.githubusercontent.com/dr-hoseyn/backpack-smart-router/main/install-online.sh | bash
```

اسکریپت آخرین نسخهٔ منتشرشدهٔ مناسب معماری سرور را دانلود و SHA-256 آن را بررسی می‌کند. برای نصب نسخهٔ مشخص می‌توانید `BACKPACK_ROUTER_VERSION` را در محیط `bash` تعیین کنید. پس از نصب، آدرس پیش‌فرض پنل و در نصب تازه نام کاربری `admin` و رمز تصادفی اولیه در همان ترمینال نمایش داده می‌شوند. در ارتقا، رمز فعلی حفظ می‌شود.

### نصب دستی از بستهٔ انتشار

بستهٔ معماری میزبان و فایل `SHA256SUMS` را از صفحهٔ انتشار بگیرید. مقدار SHA-256 بسته را با فایل انتشار مقایسه کنید. [بسته‌های انتشار](https://github.com/dr-hoseyn/backpack-smart-router/releases)

```sh
sha256sum backpack-smart-router-v0.1.0-linux-amd64.tar.gz
tar -xzf backpack-smart-router-v0.1.0-linux-amd64.tar.gz
cd backpack-smart-router-v0.1.0-linux-amd64
sudo ./install.sh
```

نصاب رمز اولیهٔ حساب `admin` را یک‌بار نمایش می‌دهد. آن را در اولین ورود تغییر دهید. پنل به‌صورت اولیه روی `127.0.0.1:9080` است. از کامپیوتر مدیریت یک SSH port forward بسازید و سپس در مرورگر به همان آدرس محلی بروید.

```sh
ssh -L 9080:127.0.0.1:9080 ops@router.example.net
```

برای پنل روی آدرس خصوصی، در `/etc/backpack-router/panel.env` نشانی خصوصی و مسیر گواهی و کلید را با گزینه‌های `-listen`، `-tls-cert` و `-tls-key` تعریف کنید. حساب سرویس `backpack-panel` باید امکان خواندن فایل‌های TLS را داشته باشد. طول نشست و نگهداری داده با `-session-minutes` و `-retention-days` تنظیم می‌شوند. محدودیت فایروال پنل را روی شبکهٔ مدیریت بگذارید.

### پنل HTTPS روی IP عمومی VPS

برای استفاده از IP عمومی، پنل را روی `127.0.0.1:9080` با TLS اجرا کنید و از نمونه‌های `deploy/backpack-router-public.socket.example` و `deploy/backpack-router-public.service.example` برای انتقال اتصال استفاده کنید. گواهی باید برای آدرس یا نامی که در مرورگر وارد می‌کنید معتبر باشد. در نمونهٔ socket، IP نمونه را با IP عمومی VPS جایگزین کنید. فایل‌های ویرایش‌شده را بدون پسوند `.example` در `/etc/systemd/system/` نصب کنید و پس از بررسی تنظیمات TLS اجرا کنید.

```sh
sudo systemctl daemon-reload
sudo systemctl restart backpack-router-panel
sudo systemctl enable --now backpack-router-public.socket
systemctl show --property=Listen --value backpack-router-public.socket
```

نصاب آنلاین پس از نصب، نشانی HTTPS را از socket روشن می‌خواند و چاپ می‌کند. نمونهٔ socket به‌صورت پیش‌فرض فعال نمی‌شود.

## تنظیم شش ورودی

1. در پنل، آدرس و پورت خصوصی واقعی هر VM را ثبت کنید. مثال‌ها غیرفعال‌اند. وزن را متناسب با ظرفیت تنظیم کنید. مقدار `0` در «سقف جریان هم‌زمان» و «سقف جریان» یعنی روتر Go سقف نرم‌افزاری اعمال نمی‌کند. سقف‌های مثبت پیکربندی‌های قدیمی با همان مقدار عمل می‌کنند.
2. پورت عمومی اولیهٔ TCP در نمونه `14443` است. پورت TCP یا UDP دلخواه را با فرایند تغییر شنونده در پایین تنظیم کنید. در فایروال فقط پورت‌هایی را که سرویس استفاده می‌کند برای کاربران باز کنید. ورودی‌های خصوصی VM و پورت عامل سلامت را به میزبان روتر و شبکهٔ مدیریت محدود کنید.
3. یک پروفایل واقعی v2rayNG با UUID مشترک و آدرس جدید روتر را جداگانه از هر شش VM آزمایش کنید. برای این آزمایش در هر مرحله فقط همان VM را فعال کنید. نام TLS، مسیر WebSocket یا گزینه‌های دیگر را در همان پروفایل واقعی بررسی کنید. سپس همهٔ ورودی‌های آزموده را فعال کنید.
4. در حالت عادی، `Weighted Least Connections` پیش‌فرض است. برای تعمیر یک مسیر، گزینهٔ «تخلیه» را بزنید و پس از اتمام کار آن را بازگردانید.

فورواردینگ از سوکت روتر به VM استفاده می‌کند و VM آدرس شبکه‌ای روتر را به‌عنوان مبدأ می‌بیند. ترافیک برنامه و داده‌های احراز هویت داخل اتصال دست‌نخورده می‌مانند. UDP را وقتی پورت ورودی واقعی UDP است فعال کنید. بعضی پروفایل‌ها ترافیک UDP داخل اتصال TCP حمل می‌کنند.

برای افزودن VM به شبکهٔ خصوصی، ابتدا رنج همان شبکه را در IP Pool مجازی‌ساز تعریف کنید. IP خصوصی یکتا را به رابط دوم VM بدهید و مسیر پیش‌فرض عمومی آن را حفظ کنید. قبل از فعال کردن مقصد در پنل، از روتر دسترسی به TCP روی IP خصوصی را بررسی کنید و با همان پروفایل واقعی تونل، یک اتصال سرتاسری برقرار کنید.

## ارسال TCP در کرنل لینوکس

این حالت اختیاری برای روتر اختصاصی IPv4 با رابط‌های عمومی و خصوصی جداست. VMها باید TCP را روی پورت `443` بپذیرند. جدول `nftables` پیش‌فرض ارسال IPv4 را می‌بندد و فقط جریان‌های همین سرویس را عبور می‌دهد. NAT از IP خصوصی روتر به‌عنوان مبدأ استفاده می‌کند. سرویس Go برای پایش سلامت، پنل و مسیر پشتیبان لازم است.

1. در نمونه‌های `deploy/kernel-forward.example` و `deploy/ingress-443.nft.example` آدرس‌ها و رابط‌ها را با مقادیر روتر جایگزین کنید. مقصدهای نمونهٔ موتور Go را هم روی همان VMها و پورت `443` تنظیم کنید. نمونهٔ مسیر پشتیبان، ورودی `443` را به شنوندهٔ Go روی `14443` منتقل می‌کند. اگر روتر مسیر پشتیبان دیگری دارد، از قواعد و سرویس همان میزبان استفاده کنید.
2. از بستهٔ استخراج‌شده، فایل‌ها را روی میزبان نصب کنید و سهم اولیه را `0` بگذارید.

```sh
sudo install -D -m 0755 deploy/kernel-forward.py /opt/backpack-router/kernel-forward/kernel-forward.py
sudo install -m 0644 deploy/kernel-forward.example /etc/backpack-router/kernel-forward.json
sudo install -m 0644 deploy/ingress-443.nft.example /etc/backpack-router/ingress-443.nft
sudo install -m 0644 deploy/backpack-router-{ingress-redirect,kernel-forward,kernel-metrics}.service /etc/systemd/system/
printf '0\n' | sudo tee /etc/backpack-router/kernel-forward.share
sudo nft -c -f /etc/backpack-router/ingress-443.nft
sudo systemctl daemon-reload
sudo systemctl enable --now backpack-router-kernel-forward backpack-router-kernel-metrics
```

3. ابتدا مسیر Go را با کلاینت واقعی آزمایش کنید. مقدار سهم را در بازهٔ `0` تا `1000` تنظیم کنید. مثلاً `50` یعنی `5%` از اتصال‌های جدید. بعد از بررسی CPU و تأخیر در بار مشابه، سهم را تدریجی افزایش دهید. `1000` تمام اتصال‌های جدید را به مسیر کرنل می‌فرستد. توقف `backpack-router-kernel-forward` اتصال‌های جدید را به Go برمی‌گرداند و نشست‌های برقرار را در جدول موجود نگه می‌دارد.
4. برای جمع ترافیک دو مسیر در داشبورد، `-kernel-metrics /run/backpack-router-kernel/traffic.json` را به `PANEL_ARGS` اضافه و پنل را ری‌استارت کنید. آمار اتصال فعال همچنان مربوط به Go است. آمار ترافیک کرنل از زمان فعال شدن شمارنده‌ها محاسبه می‌شود.

اگر کارت خصوصی تک‌صف است و وقفه‌ها یک هسته را اشباع کرده‌اند، نمونهٔ `deploy/kernel-forward-rps.conf.example` نحوهٔ ثبت RPS در systemd را نشان می‌دهد. ماسک `2f` برای میزبان شش‌هسته‌ای با وقفهٔ کارت خصوصی روی `CPU 4` است. پیش از استفاده، رابط، صف و ماسک را با وضعیت IRQ میزبان تطبیق دهید. فایل تنظیم‌شده را در `/etc/systemd/system/backpack-router-kernel-forward.service.d/10-private-rps.conf` قرار دهید و `systemctl daemon-reload` را اجرا کنید.

برای اندازه‌گیری ترافیک Go و کرنل، CPU و تعداد ورودی‌های conntrack، ابزار زیر را با پیکربندی همان روتر اجرا کنید. این فرمان فقط شمارنده‌ها را می‌خواند.

```sh
sudo /opt/backpack-router/kernel-forward/kernel-forward.py /etc/backpack-router/kernel-forward.json measure --seconds 15
```

### جابه‌جایی مرحله‌ای بین دو موتور Go

برای تعویض موتور زیر بار، نمونهٔ `deploy/backpack-router-engine-staged.service.example` موتور دوم را با سوکت کنترل جدا اجرا می‌کند. پیکربندی خودتان را در مسیر `engine-staged.json` از نمونهٔ سرویس قرار دهید. ورودی TCP موتور دوم را روی `0.0.0.0:14444` تنظیم کنید و پورت‌های هر گروه دیگر را هم با موتور اول تطبیق دهید تا تداخل نداشته باشند. بعد از راه‌اندازی موتور دوم و بررسی سلامت مقصدها، `deploy/ingress-staged.nft.example` هدایت `443` و `14443` و مسدود کردن دسترسی مستقیم عمومی به `14444` را در یک جدول انجام می‌دهد.

قبل از اعمال، فایل `nftables` را با نشانی و رابط واقعی روتر و دستور `nft -c -f` بررسی کنید. فایل را در یک تراکنش اعمال کنید. اگر جدول `backpack_router_ingress` از قبل وجود دارد، حذف تعریف قبلی جدول و افزودن تعریف جدید را در همان فایل تراکنش قرار دهید تا بازهٔ بدون قاعده ایجاد نشود. ورودی‌های conntrack را در زمان تغییر پاک نکنید تا نشست‌های اولیه روی موتور قبلی بمانند. برای استفاده از موتور دوم پس از بوت، سرویس ingress را بعد از آن اجرا کنید و نشانی سوکت سلامت در پیکربندی کرنل و گزینه‌های `-socket` و `-config` پنل را با موتور فعال هماهنگ کنید. در انتقال زنده، موتور قبلی را پس از تخلیهٔ نشست‌هایش متوقف کنید.

## آزمون سلامت سرتاسری

روی هر VM یک SOCKS5 محلی روی loopback فراهم کنید که اتصال به یک مقصد HTTPS تحت کنترل در آلمان را از **همان تونل** عبور می‌دهد. مقصد باید با گواهی معتبر TLS پاسخ HTTP 204 بدهد. برای نمونه از کلاینت محلیِ پروفایل همان VM به‌عنوان SOCKS استفاده کنید. پاسخ مستقیم از اینترنت VM اعتبار این آزمون را از بین می‌برد.

```sh
sudo ./install.sh --agent
sudoedit /etc/backpack-router/agent.env
sudo systemctl enable --now backpack-router-agent
```

آدرس خصوصی عامل همان VM را در پنل وارد کنید و «آزمون» را بزنید. برای تأیید، در پایلوت مسیر آلمان را با باز ماندن پورت ورودی قطع کنید. وضعیت باید بعد از آستانهٔ خرابی تغییر کند. زمان گزارش‌شده برای مسیر دارای عامل شامل برقراری اتصال، TLS و پاسخ HTTP است. بدون عامل، پنل نتیجهٔ آزمون پورت TCP را نشان می‌دهد و برای ورودی UDP-only عامل الزامی است. شاخص‌های CPU، RAM و ارسال مجدد TCP در محدودهٔ VM محاسبه می‌شوند. نرخ ترافیک به بایت‌های عبوری از روتر مربوط است. افت بسته فقط در صورت وجود گزارش اندازه‌گیری نمایش داده می‌شود.

## هدایت یک پورت عمومی TCP به مقصد خصوصی

اگر پروفایل VLESS روی پورت عمومی `8443` تنظیم شده و موتور روی پورت دیگری گوش می‌دهد، می‌توانید یک سوکت جدا برای `8443` فعال کنید. این سوکت TCP را به یک مقصد خصوصی می‌فرستد و TLS روی همان مقصد برقرار می‌شود. ابتدا از روتر بررسی کنید که IP و پورت خصوصی پاسخ می‌دهند و مقصد برای SNI پروفایل گواهی معتبر ارائه می‌کند.

در [نمونهٔ سوکت](deploy/backpack-router-tcp-relay.socket.example)، نشانی `ListenStream` را با IP عمومی روتر و پورت پروفایل جایگزین کنید. در [نمونهٔ سرویس](deploy/backpack-router-tcp-relay.service.example)، آدرس انتهای `ExecStart` را روی IP و پورت خصوصی مقصد بگذارید. پورت عمومی باید آزاد باشد. فایل‌های ویرایش‌شده را از داخل بستهٔ انتشار نصب کنید.

```sh
sudo install -m 0644 deploy/backpack-router-tcp-relay.socket.example /etc/systemd/system/backpack-router-tcp-relay.socket
sudo install -m 0644 deploy/backpack-router-tcp-relay.service.example /etc/systemd/system/backpack-router-tcp-relay.service
sudo systemctl daemon-reload
sudo systemctl enable --now backpack-router-tcp-relay.socket
systemctl show --property=Listen --value backpack-router-tcp-relay.socket
```

اتصال را با همان پروفایل از بیرون روتر آزمایش کنید. این مسیر یک مقصد ثابت دارد و ترافیک آن از موتور Go عبور نمی‌کند. برای توزیع ترافیک یک پورت بین چند مقصد، پورت را در تنظیمات موتور از روش بخش بعدی تعریف کنید.

نمونهٔ سرویس حداکثر ۴۰۹۶ اتصال هم‌زمان می‌پذیرد و سقف فایل‌های باز آن ۳۲۷۶۸ است. اگر ظرفیت اتصال را تغییر می‌دهید، برای سوکت‌ها و لوله‌های هر اتصال هم در `LimitNOFILE` جا بگذارید.

## تغییر پورت یا گروه

تغییر ورودی شنونده به راه‌اندازی مجدد موتور نیاز دارد. یک نسخهٔ پیکربندی اصلاح‌شده در مسیر قابل خواندن برای `backpack-panel` قرار دهید و در پنجرهٔ نگهداری اجرا کنید.

```sh
sudo /opt/backpack-router/current/backpack-router check -config /path/to/new-engine.json
sudo systemctl stop backpack-router-panel backpack-router-engine
sudo -u backpack-panel -g backpack-router /opt/backpack-router/current/backpack-router restore-config -from /path/to/new-engine.json
sudo systemctl start backpack-router-engine backpack-router-panel
```

## ارتقا، پشتیبان و بازگشت

- پیش از ارتقا از SQLite و پیکربندی نسخه بگیرید. برای SQLite از دستور زیر استفاده کنید. علاوه بر دیتابیس، از پوشهٔ `/etc/backpack-router` و فایل `/var/lib/backpack-router/config/engine.json` در مقصد خصوصی نسخه بگیرید. نسخه‌های پشتیبان دارای هش رمز مدیران و نشانی شبکه‌اند.

```sh
sudo /opt/backpack-router/current/backpack-router backup -out /root/router-backup.sqlite
```

- برای بازیابی SQLite، ابتدا پنل را متوقف کنید. فایل‌های `router.sqlite`، `router.sqlite-wal` و `router.sqlite-shm` موجود را به یک پوشهٔ پشتیبان منتقل کنید. سپس با دستور زیر دیتابیس را بازگردانید. نسخهٔ پیکربندی متناظر را با فرایند Restore در بخش تغییر پورت یا گروه اعمال کنید تا ترتیب نسخه‌ها با موتور هماهنگ شود.

```sh
sudo install -o backpack-panel -g backpack-router -m 0600 /root/router-backup.sqlite /var/lib/backpack-router/panel/router.sqlite
```

- برای ارتقا بستهٔ جدید را استخراج و `sudo ./install.sh` را اجرا کنید. نصب‌کننده پیکربندی و دادهٔ پنل را حفظ می‌کند. ری‌استارت موتور روی اتصال‌های باز اثر دارد. برای بازگشت دستی، پیوند `/opt/backpack-router/current` را در پنجرهٔ نگهداری به نسخهٔ مناسب در `/opt/backpack-router/releases` برگردانید و سرویس‌ها را ری‌استارت کنید.
- برای پاک کردن سرویس‌ها از `sudo ./deploy/uninstall.sh` در بستهٔ انتشار استفاده کنید. فایل‌های تنظیمات و داده روی دیسک باقی می‌مانند.

## پایلوت ظرفیت

در زیرساخت آزمایشی نمایندهٔ محیط واقعی، بار ۵٬۰۰۰ اتصال هم‌زمان و ۱ Gbps را ابتدا بدون روتر و سپس از طریق روتر تولید کنید. پهنای باند موفق، p99 زمان اتصال و پاسخ، CPU، RSS، افت UDP، نرخ اتصال موفق و بازیابی پس از خرابی را در هر دو حالت ثبت کنید. بودجهٔ اولیهٔ روتر ۲ vCPU و ۲ GB RAM است. سقف جریان نرم‌افزار، محدودیت توصیفگر فایل، ظرفیت لینک و سقف VM را در گزارش کنار هم قرار دهید. بنچمارک loopback در CI برای مقایسهٔ نسخه‌هاست. ظرفیت واقعی به نتیجهٔ آزمون شش تونل وابسته است.
