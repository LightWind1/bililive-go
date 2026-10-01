import React, { useState, useEffect, useRef, useCallback } from 'react';
import { Button, Spin, Input, Badge, Alert, Divider, notification } from 'antd';
import API from '../../utils/api';
import './edit-cookie.css';

const { TextArea } = Input;

interface DouyuLoginPanelProps {
    initialCookie: string;
    /** 兼容父组件签名，斗鱼面板改为只读展示后不再回写 Cookie */
    onCookieChange?: (cookie: string) => void;
    /** 扫码登录成功后由父组件重新拉取服务端 Cookie 并同步显示基准 */
    onScanSuccess?: () => void;
    api: API;
}

type QRStatus = 'loading' | 'active' | 'scanned' | 'expired' | 'success';

interface DouyuIdentity {
    uid: string;
    nickname: string;
}

// 后端 /douyu/auth/status 返回的自动续期健康状态（不含凭证原文）
interface DouyuAuthStatus {
    logged_in: boolean;
    has_ltp0: boolean;
    need_rescan: boolean;
    next_refresh_at: number;
}

const formatStamp = (unixSec: number): string =>
    new Date(unixSec * 1000).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });

/**
 * 斗鱼扫码登录面板。
 * 与 B 站面板不同：确认登录后由后端自动完成 cookie 换取与保存，
 * 前端只负责展示二维码、轮询状态并提示结果。
 * 斗鱼没有独立的用户信息校验接口，登录身份直接从 cookie 的
 * acf_uid / acf_nickname 字段解析。
 */

// 从 cookie 串中解析斗鱼登录身份（acf_uid / acf_nickname，后者为 URL 编码）
const parseDouyuIdentity = (cookieStr: string): DouyuIdentity | null => {
    if (!cookieStr) return null;
    const get = (name: string): string => {
        const match = cookieStr.match(new RegExp(`(?:^|;)\\s*${name}=([^;]*)`));
        return match ? match[1].trim() : '';
    };
    const uid = get('acf_uid');
    if (!uid) return null;
    const rawNick = get('acf_nickname');
    let nickname = rawNick;
    try {
        nickname = decodeURIComponent(rawNick);
    } catch (e) {
        // 保持原值
    }
    return { uid, nickname };
};

// 解析 acf_jwt_token（JWT，exp 签发时固定为 7 天后）payload 的过期时间，返回毫秒时间戳；
// 无 token 或非标准 JWT 时返回 null
const parseDouyuCookieExpiry = (cookieStr: string): number | null => {
    if (!cookieStr) return null;
    const match = cookieStr.match(/(?:^|;)\s*acf_jwt_token=([^;]+)/);
    if (!match) return null;
    const parts = match[1].trim().split('.');
    if (parts.length !== 3) return null;
    try {
        let b64 = parts[1].replace(/-/g, '+').replace(/_/g, '/');
        b64 += '='.repeat((4 - (b64.length % 4)) % 4);
        const payload = JSON.parse(atob(b64));
        return typeof payload.exp === 'number' ? payload.exp * 1000 : null;
    } catch (e) {
        return null;
    }
};

const DouyuLoginPanel: React.FC<DouyuLoginPanelProps> = ({ initialCookie, onScanSuccess, api }) => {
    const [qrCodeUrl, setQrCodeUrl] = useState('');
    const [loginStatus, setLoginStatus] = useState<QRStatus>('loading');
    const [loginMsg, setLoginMsg] = useState('正在获取二维码...');
    const [loginUser, setLoginUser] = useState<DouyuIdentity | null>(null);
    const [textView, setTextView] = useState(initialCookie);
    const [authStatus, setAuthStatus] = useState<DouyuAuthStatus | null>(null);

    const pollTimerRef = useRef<any>(null);
    const isMounted = useRef(true);
    // 连续轮询失败计数：后端持续异常（如网络中断）时避免每 2s 无限重试
    const pollErrorCountRef = useRef(0);
    // 上一次轮询是否仍未返回：上游超时 15s 而间隔只有 2s，不挡住会堆积并发请求
    const pollInFlightRef = useRef(false);
    // 二维码有效期截止时间戳：斗鱼侧 error 码长期不回 -1 时，前端也必须自行停止轮询
    const qrDeadlineRef = useRef(0);
    // 轮询会话代号：刷新二维码会开启新一轮轮询，而上一轮的请求可能仍在路上（上游超时可达 15s）。
    // 不做隔离的话，迟到的旧响应会 stopPolling() 停掉刚起的新定时器，并把界面打成"已失效/连续失败"。
    const pollSessionRef = useRef(0);

    const stopPolling = useCallback(() => {
        if (pollTimerRef.current) {
            clearInterval(pollTimerRef.current);
            pollTimerRef.current = null;
        }
        pollInFlightRef.current = false;
    }, []);

    // 二维码到期/连续失败后的统一收尾：停止轮询并提示刷新
    const giveUpPolling = useCallback((msg: string) => {
        stopPolling();
        setLoginStatus('expired');
        setLoginMsg(msg);
    }, [stopPolling]);

    // 父组件在扫码成功后会重新拉取服务端 cookie 并更新 initialCookie，同步到本地显示
    useEffect(() => {
        setTextView(initialCookie);
    }, [initialCookie]);

    // 拉取后端自动续期状态（续期失败提醒、下次续期时间）
    const fetchAuthStatus = useCallback(() => {
        api.getDouyuAuthStatus()
            .then((res: any) => {
                if (isMounted.current && res.err_no === 0 && res.data) setAuthStatus(res.data);
            })
            .catch(() => {
                // 该接口只用于展示提示，失败时静默降级为不显示，不打断扫码主流程
            });
    }, [api]);

    useEffect(() => {
        fetchAuthStatus();
        // 低频刷新：续期由后台按需触发（约每 3 天一次；临时失败按 6 小时退避，判定凭证失效后 24 小时才再试），
        // 面板开着不动的话"已自动续期/已判定失效"永远看不到。60s 一次足够，且接口只读配置、无外部请求。
        const timer = setInterval(fetchAuthStatus, 60000);
        return () => clearInterval(timer);
    }, [fetchAuthStatus]);

    const startPolling = useCallback((code: string) => {
        stopPolling();
        const session = ++pollSessionRef.current;
        pollErrorCountRef.current = 0;
        pollTimerRef.current = setInterval(() => {
            if (pollInFlightRef.current) return; // 上一次还没回来就先跳过，避免请求堆积
            if (qrDeadlineRef.current && Date.now() > qrDeadlineRef.current) {
                giveUpPolling('二维码已超时失效，请点击下方按钮刷新');
                return;
            }
            pollInFlightRef.current = true;
            api.pollDouyuQRCode(code)
                .then((res: any) => {
                    // 迟到的旧会话响应一律丢弃：它既不能改状态，也不该重置新会话的在途标记
                    if (!isMounted.current || session !== pollSessionRef.current) return;
                    pollInFlightRef.current = false;
                    // 后端统一响应格式为 {err_no, err_msg, data}
                    if (res.err_no !== 0 || !res.data) {
                        pollErrorCountRef.current += 1;
                    } else {
                        pollErrorCountRef.current = 0;
                    }
                    if (pollErrorCountRef.current >= 5) {
                        giveUpPolling('登录状态查询连续失败，请刷新二维码重试');
                        return;
                    }
                    if (res.err_no !== 0 || !res.data) return;
                    const data = res.data;
                    switch (data.state) {
                        case 'waiting':
                            setLoginStatus('active');
                            setLoginMsg('请用斗鱼 App 扫码');
                            break;
                        case 'scanned':
                            setLoginStatus('scanned');
                            setLoginMsg('已扫码，请在手机上确认登录');
                            break;
                        case 'success':
                            stopPolling();
                            setLoginStatus('success');
                            setLoginMsg('登录成功，Cookie 已自动保存并生效');
                            setLoginUser({ uid: data.uid, nickname: data.nickname });
                            // 后端不回传 cookie 原文，重新拉取服务端最新值刷新 TextArea，
                            // 避免用户基于过期的旧文本编辑并保存、覆盖掉新登录态
                            onScanSuccess?.();
                            fetchAuthStatus();
                            notification.success({
                                message: '斗鱼登录成功',
                                description: '登录 Cookie 已保存，新的录制将携带登录态（可消除约 5 分钟的匿名断流）',
                            });
                            break;
                        case 'expired':
                            stopPolling();
                            setLoginStatus('expired');
                            setLoginMsg('二维码已失效，请点击下方按钮刷新');
                            break;
                        case 'rejected':
                            stopPolling();
                            setLoginStatus('expired');
                            setLoginMsg('登录被取消或拒绝，请刷新二维码重试');
                            break;
                        case 'save_failed':
                            // 手机端已确认、一次性登录票据已消耗：反复重扫没用，必须先让配置文件可写
                            stopPolling();
                            setLoginStatus('expired');
                            setLoginMsg('登录已在手机上确认成功，但写入配置文件失败，请检查磁盘空间与配置文件是否可写后重新扫码' + (data.msg ? `（${data.msg}）` : ''));
                            break;
                        default:
                            // 未识别的状态（后端/上游接口变更）必须可见，否则用户只看到"请扫码"却永远扫不上
                            giveUpPolling('登录状态无法识别' + (data.msg ? `：${data.msg}` : '') + '，请刷新二维码重试');
                            break;
                    }
                })
                .catch(() => {
                    if (!isMounted.current || session !== pollSessionRef.current) return;
                    pollInFlightRef.current = false;
                    // 后端把上游失败统一回成 HTTP 5xx，此时 Promise 直接 reject，
                    // 上面的 err_no 计数走不到；不在此处计数的话熔断逻辑就是死代码，会每 2s 无限重试。
                    pollErrorCountRef.current += 1;
                    if (pollErrorCountRef.current >= 5) {
                        giveUpPolling('登录状态查询连续失败，请检查网络后刷新二维码重试');
                    }
                });
        }, 2000);
    }, [api, stopPolling, giveUpPolling, onScanSuccess, fetchAuthStatus]);

    const getDouyuQRCode = useCallback(() => {
        // 先断掉旧会话：正在路上的上一轮轮询响应不能再回来改状态
        stopPolling();
        pollSessionRef.current += 1;
        // 清掉旧二维码图：加载中仍挂着上一张时，用户扫到的是已经作废的码
        setQrCodeUrl('');
        setLoginStatus('loading');
        setLoginMsg('正在获取二维码...');
        setLoginUser(null);
        qrDeadlineRef.current = 0;
        api.getDouyuQRCode()
            .then((res: any) => {
                if (!isMounted.current) return;
                if (res.err_no === 0 && res.data) {
                    setQrCodeUrl(res.data.qr_url);
                    setLoginStatus('active');
                    setLoginMsg('请用斗鱼 App 扫码');
                    // 二维码有有效期（斗鱼回 300 秒）：上游状态异常时前端也要自行到期停止轮询
                    const expire = Number(res.data.expire) || 0;
                    qrDeadlineRef.current = expire > 0 ? Date.now() + expire * 1000 : 0;
                    startPolling(res.data.code);
                } else {
                    setLoginStatus('expired');
                    setLoginMsg('获取二维码失败: ' + (res.err_msg || '未知错误'));
                }
            })
            .catch(err => {
                if (!isMounted.current) return;
                setLoginStatus('expired');
                setLoginMsg('获取二维码失败，请检查网络');
                console.error(err);
            });
    }, [api, startPolling, stopPolling]);

    useEffect(() => {
        isMounted.current = true;
        getDouyuQRCode();
        return () => {
            isMounted.current = false;
            stopPolling();
        };
    }, [getDouyuQRCode, stopPolling]);

    // 验证卡片展示的身份：优先扫码结果，其次从当前 Cookie 文本实时解析
    const displayUser = loginUser || parseDouyuIdentity(textView);
    const scanSucceeded = loginStatus === 'success';

    // 后台续期状态：needRescan 表示 safeAuth 换票已判定凭证失效，必须由用户重新扫码
    const needRescan = !!authStatus?.need_rescan;
    const hasLtp0 = !!authStatus?.has_ltp0;
    const nextRenewAt = authStatus?.next_refresh_at || 0;
    // 服务端才是"当前登录态是否可用"的权威（见 statusLine）。本地 TextArea 里的文本可能已经被
    // 后台续期换掉或被清空，只按它算有效期会和红字状态打架，所以红字时不再展示有效期行。
    const serverSaysUsable = authStatus === null || authStatus.logged_in;
    // 扫码成功后，父组件还要异步把新 cookie 拉回来；这中间 TextArea 里仍是扫码前的旧文本，
    // 按旧文本算出的"已过期"会和"登录成功"同屏。等文本确实换成这次登录的账号后再展示。
    const cookieTextIsStale = scanSucceeded && !!loginUser && displayUser?.uid !== loginUser.uid;

    // 登录态有效期：来自 acf_jwt_token 的 exp（斗鱼签发时固定 7 天）
    const expiryMs = parseDouyuCookieExpiry(textView);
    let expiryInfo: { text: string; color: string } | null = null;
    if (expiryMs !== null && serverSaysUsable && !cookieTextIsStale) {
        const remainDays = (expiryMs - Date.now()) / 86400000;
        const until = new Date(expiryMs).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
        if (remainDays <= 0) {
            // needRescan 才是"续期失败"的权威信号（顶部红色 Alert 与状态行都按它显示）。
            // 只要还绑着长期凭证且后端没判失效，本地文本的 exp 过期多半只是这段文本没跟着刷新，
            // 再喊"请重新扫码登录"就会和绿色的"检测到有效登录 Cookie"同屏自相矛盾。
            // authStatus 尚未返回时同样不能断言"续期未成功"，口径与 ltp0Missing 保持一致。
            expiryInfo = authStatus === null || (hasLtp0 && !needRescan)
                ? { text: `本地 Cookie 文本的签名已于 ${until} 到期，登录态以后端自动续期状态为准`, color: '#8c8c8c' }
                : { text: `Cookie 已于 ${until} 过期，自动续期未成功，请重新扫码登录`, color: '#ff4d4f' };
        } else if (remainDays <= 2) {
            expiryInfo = { text: `Cookie 将于 ${until} 过期（剩余不足 2 天），系统会自动续期`, color: '#fa8c16' };
        } else {
            expiryInfo = { text: `Cookie 有效期至 ${until}（剩余 ${Math.floor(remainDays)} 天，将自动续期）`, color: '#8c8c8c' };
        }
    }

    // 状态接口尚未返回/返回失败时 authStatus 为 null，此时不能断定"没有 LTP0"，
    // 否则会对已启用自动续期的用户误报橙色警告。
    const ltp0Missing = authStatus !== null && !hasLtp0;
    // 服务端才是登录态的权威来源：本地 Cookie 文本可能是打开面板时的旧值（例如另一处已清空/已被续期替换），
    // 只按 scanSucceeded/needRescan 回落会让"其实没登录"显示成绿色"检测到有效登录 Cookie"。
    const statusLine = scanSucceeded
        ? { text: '状态：扫码登录成功，Cookie 已自动保存并应用到运行中的房间', color: '#52c41a' }
        : needRescan
            ? { text: '状态：长期凭证已失效，自动续期未成功，请重新扫码', color: '#ff4d4f' }
            : authStatus === null
                ? { text: '状态：正在读取登录状态...', color: '#8c8c8c' }
                : authStatus.logged_in
                    ? { text: '状态：检测到有效登录 Cookie，录制将自动携带登录态', color: '#52c41a' }
                    : { text: '状态：当前没有可用的登录 Cookie，斗鱼房间为匿名录制（约 5 分钟被切断一次），请扫码登录', color: '#ff4d4f' };

    return (
        <div className="bili-login-container">
            {needRescan && (
                <Alert
                    type="error"
                    showIcon
                    style={{ marginBottom: 12 }}
                    message={<span style={{ fontWeight: 700, fontSize: '15px' }}>斗鱼自动续期失败，请重新扫码登录</span>}
                    description="长期凭证（LTP0）已失效，后台无法再自动续期；当前 Cookie 到期后斗鱼房间会退回匿名录制、约 5 分钟被切断一次。请用斗鱼 App 扫描左侧二维码，扫码成功后本提示自动撤销（若这一次斗鱼未下发新的长期凭证，自动续期需再扫一次码才会恢复）。"
                />
            )}
            <div className="bili-login-layout">
                {/* QR Section */}
                <div className="bili-qr-section">
                    <div className="section-label" style={{ borderLeft: 'none', paddingLeft: 0, justifyContent: 'center' }}>
                        斗鱼 App 扫码登录
                    </div>
                    <div className="qr-frame">
                        {loginStatus === 'loading' ? (
                            <div className="qr-overlay"><Spin tip="获取中..." /></div>
                        ) : (
                            <>
                                {/* 没有二维码 URL 时不渲染 img：src 里的 data= 为空会画出一张无意义图，
                                    用户对着它扫码只会失败 */}
                                {qrCodeUrl && (
                                    <img
                                        className="qr-image"
                                        src={`https://api.qrserver.com/v1/create-qr-code/?size=160x160&data=${encodeURIComponent(qrCodeUrl)}`}
                                        alt="QR Code"
                                    />
                                )}
                                {(loginStatus === 'scanned' || loginStatus === 'success' || loginStatus === 'expired') && (
                                    <div className="qr-overlay">
                                        <div className="qr-status-icon">
                                            {loginStatus === 'scanned' && '📱'}
                                            {loginStatus === 'success' && '✅'}
                                            {loginStatus === 'expired' && '⌛'}
                                        </div>
                                        <div className="qr-status-text">
                                            {loginStatus === 'scanned' && '已扫描，待确认'}
                                            {loginStatus === 'success' && '登录成功'}
                                            {loginStatus === 'expired' && '二维码已失效'}
                                        </div>
                                        {loginStatus === 'expired' && (
                                            <div style={{ color: '#8c8c8c', fontSize: '12px', marginTop: 4 }}>
                                                点击下方按钮获取新二维码
                                            </div>
                                        )}
                                    </div>
                                )}
                            </>
                        )}
                    </div>
                    <div className="login-msg-text">{loginMsg}</div>
                    {loginStatus === 'expired' && (
                        <Button
                            className="verify-btn"
                            size="small"
                            type="primary"
                            style={{ marginTop: 10 }}
                            onClick={getDouyuQRCode}
                        >
                            刷新二维码
                        </Button>
                    )}
                </div>

                {/* Cookie 状态展示（只读） */}
                <div className="bili-manual-section">
                    <div className="section-label">
                        <span>当前登录 Cookie（只读）</span>
                    </div>
                    <TextArea
                        className="cookie-textarea"
                        placeholder="尚无 Cookie，请在左侧扫码登录"
                        value={textView}
                        autoSize={{ minRows: 6, maxRows: 6 }}
                        readOnly
                    />
                    <div className="manual-tip">
                        斗鱼登录 Cookie 只能通过扫码获取，且会随长期凭证自动续期，无需也不能手动编辑。若下方提示已过期且持续无法自动续期，请重新扫码。
                    </div>

                    {displayUser ? (
                        <div className="verification-card">
                            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                                <div style={{ display: 'flex', alignItems: 'center' }}>
                                    <Badge status={needRescan ? 'error' : 'processing'} color={statusLine.color} />
                                    <span className="user-badge">{displayUser.nickname || '未知昵称'}</span>
                                    <span className="uid-text">UID: {displayUser.uid}</span>
                                </div>
                            </div>
                            <div style={{ fontSize: '13px', color: statusLine.color, marginTop: '8px', fontWeight: 500 }}>
                                {statusLine.text}
                            </div>
                            {expiryInfo && (
                                <div style={{ fontSize: '13px', color: expiryInfo.color, marginTop: '4px', fontWeight: 500 }}>
                                    {expiryInfo.text}
                                </div>
                            )}
                            {hasLtp0 && nextRenewAt > 0 && (
                                <div style={{ fontSize: '13px', color: needRescan ? '#ff4d4f' : '#8c8c8c', marginTop: '4px' }}>
                                    {needRescan
                                        ? `将在 ${formatStamp(nextRenewAt)} 自动重试续期，若持续失败请重新扫码`
                                        : `下次自动续期：${formatStamp(nextRenewAt)}（系统自动完成，无需操作）`}
                                </div>
                            )}
                            {ltp0Missing && (
                                <div style={{ fontSize: '13px', color: '#fa8c16', marginTop: '4px' }}>
                                    尚未记录长期凭证（LTP0），Cookie 过期后无法自动续期，建议重新扫码一次以启用自动续期
                                </div>
                            )}
                        </div>
                    ) : (
                        <div className="verification-card pending">
                            <span>
                                {textView
                                    ? '当前 Cookie 未包含登录字段（acf_uid），请重新扫码登录'
                                    : '请在左侧扫码登录'}
                            </span>
                        </div>
                    )}
                </div>
            </div>

            <Divider className="divider-text">关于斗鱼登录 Cookie</Divider>

            <Alert
                className="info-alert"
                showIcon
                message={<span style={{ fontWeight: 700, fontSize: '15px' }}>扫码登录 · 自动续期</span>}
                type="info"
                description={
                    <div style={{ fontSize: '14px' }}>
                        匿名（无 Cookie）录制会被斗鱼 CDN 约 5 分钟切断一次流并不断重开分段，需要保持登录态。斗鱼登录 Cookie 只能通过扫码获取，其使用方式如下：
                        <ul className="instruction-list">
                            <li>用手机 <b>斗鱼 App</b> 扫描左侧二维码并确认登录，Cookie 会由后端自动保存并立即生效。</li>
                            <li>主站登录 Cookie（<b>acf_*</b>）约 6 天有效；扫码时后端同时记录了长期凭证 <b>LTP0</b>，之后系统会在到期前<b>自动按需换票续期</b>（约每 3 天一次），无需你再次操作。</li>
                            <li>只有当长期凭证也失效（页面顶部出现红色"自动续期失败"提示）时，才需要重新扫码一次。</li>
                            <li>因 Cookie 会持续轮换、且长期凭证仅能由扫码引导，<b>不再支持手动粘贴 Cookie</b>（手填的 Cookie 约 6 天后过期且无法自动续期）。</li>
                        </ul>
                    </div>
                }
            />
        </div>
    );
};

export default DouyuLoginPanel;
