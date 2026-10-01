import React, { useState, useEffect, useRef, useCallback } from 'react';
import {
  Modal,
  Input,
  Button,
  Upload,
  Progress,
  Space,
  Typography,
  Radio,
  Alert,
  Switch,
  message as antdMessage,
} from 'antd';
import {
  UploadOutlined,
  CheckCircleOutlined,
  CloseCircleOutlined,
  MinusCircleOutlined,
} from '@ant-design/icons';
import API from '../../utils/api';
import { sseManager } from '../../utils/sse';

const { TextArea } = Input;
const { Text } = Typography;

const api = new API();

interface BatchResultItem {
  url: string;
  status: 'pending' | 'success' | 'error';
  error?: string;
}

interface Props {
  visible: boolean;
  onClose: () => void;
  onSuccess: () => void;
}

type InputMode = 'single' | 'batch';
type ImportMode = 'text' | 'file';

function generateBatchId(): string {
  return 'batch_' + Date.now() + '_' + Math.random().toString(36).substring(2, 10);
}

const MAX_BATCH_URLS = 50;

const AddRoomDialog: React.FC<Props> = ({ visible, onClose, onSuccess }) => {
  const [inputMode, setInputMode] = useState<InputMode>('single');
  const [importMode, setImportMode] = useState<ImportMode>('text');
  const [singleUrl, setSingleUrl] = useState('');
  const [textInput, setTextInput] = useState('');
  const [listen, setListen] = useState(true);
  const [notifyOnly, setNotifyOnly] = useState(false);
  const [processing, setProcessing] = useState(false);
  const [current, setCurrent] = useState(0);
  const [total, setTotal] = useState(0);
  const [results, setResults] = useState<BatchResultItem[]>([]);
  const [completed, setCompleted] = useState(false);
  const [successCount, setSuccessCount] = useState(0);
  const [failCount, setFailCount] = useState(0);

  const sseSubRef = useRef<string[]>([]);
  const sseTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const parseUrls = useCallback((text: string): string[] => {
    return text
      .split(/[\r\n]+/)
      .map(line => line.trim())
      .filter(line => line.length > 0 && !line.startsWith('#') && !line.startsWith('＃'));
  }, []);

  const handleFileImport = useCallback((file: File) => {
    const reader = new FileReader();
    reader.onload = (e) => {
      const text = e.target?.result as string;
      setTextInput(text);
      setImportMode('text');
    };
    reader.onerror = () => {
      antdMessage.error('文件读取失败');
    };
    reader.readAsText(file);
    return false;
  }, []);

  const cleanupSSE = useCallback(() => {
    sseSubRef.current.forEach(id => sseManager.unsubscribe(id));
    sseSubRef.current = [];
    if (sseTimeoutRef.current) {
      clearTimeout(sseTimeoutRef.current);
      sseTimeoutRef.current = null;
    }
  }, []);

  const subscribeSSE = useCallback((batchId: string) => {
    const progressSubId = sseManager.subscribe(
      batchId,
      'batch_progress',
      (message) => {
        const data = message.data;
        setCurrent(data.index);
        setResults(prev => prev.map((item, i) =>
          i === data.index
            ? { ...item, status: data.success ? 'success' : 'error', error: data.error }
            : item
        ));
      }
    );

    const completeSubId = sseManager.subscribe(
      batchId,
      'batch_complete',
      (message) => {
        const data = message.data;
        setProcessing(false);
        setCompleted(true);
        setSuccessCount(data.success_count);
        setFailCount(data.fail_count);
        cleanupSSE();

        if (data.fail_count === 0) {
          if (data.persist_error) {
            antdMessage.warning('添加成功，但配置保存失败，重启后可能丢失');
          } else {
            antdMessage.success(`添加成功`);
          }
        } else if (data.success_count === 0) {
          antdMessage.error(`添加失败`);
        } else {
          antdMessage.warning(`添加完成，成功 ${data.success_count} 个，失败 ${data.fail_count} 个`);
        }
        onSuccess();
      }
    );

    sseSubRef.current = [progressSubId, completeSubId];

    // 5 分钟超时保护：后端异常时避免 UI 永久卡住
    sseTimeoutRef.current = setTimeout(() => {
      antdMessage.error('批量添加超时，请刷新页面查看结果');
      setProcessing(false);
      setCompleted(true);
      cleanupSSE();
    }, 5 * 60 * 1000);
  }, [cleanupSSE, onSuccess]);

  useEffect(() => {
    return cleanupSSE;
  }, [cleanupSSE]);

  const handleStart = async () => {
    if (processing) return;

    // 根据模式获取 URL 列表
    let urls: string[];
    if (inputMode === 'single') {
      const trimmed = singleUrl.trim();
      if (!trimmed) {
        antdMessage.warning('请输入直播间地址');
        return;
      }
      urls = [trimmed];
    } else {
      urls = parseUrls(textInput);
      if (urls.length === 0) {
        antdMessage.warning('请输入至少一个直播间地址');
        return;
      }
      if (urls.length > MAX_BATCH_URLS) {
        antdMessage.warning(`单次最多添加 ${MAX_BATCH_URLS} 个直播间，当前 ${urls.length} 个`);
        return;
      }
    }

    const batchId = generateBatchId();

    setProcessing(true);
    setCompleted(false);
    setCurrent(-1);
    setTotal(urls.length);
    setResults(urls.map(url => ({ url, status: 'pending' as const })));
    setSuccessCount(0);
    setFailCount(0);

    subscribeSSE(batchId);

    try {
      await api.batchAddRooms(urls, listen, notifyOnly, batchId);
    } catch (err: any) {
      antdMessage.error('请求失败: ' + (err?.message || err));
      cleanupSSE();
      setProcessing(false);
    }
  };

  const handleClose = () => {
    if (processing) {
      Modal.confirm({
        title: '确认关闭',
        content: '关闭后前端不再显示进度，后端会继续完成剩余添加。确认关闭？',
        onOk: () => {
          cleanupSSE();
          resetState();
          onClose();
        },
      });
      return;
    }
    resetState();
    onClose();
  };

  const resetState = () => {
    cleanupSSE();
    setInputMode('single');
    setImportMode('text');
    setSingleUrl('');
    setTextInput('');
    setListen(true);
    setNotifyOnly(false);
    setProcessing(false);
    setCurrent(0);
    setTotal(0);
    setResults([]);
    setCompleted(false);
    setSuccessCount(0);
    setFailCount(0);
  };

  const progressPercent = total > 0 && current >= 0 ? Math.round(((current + 1) / total) * 100) : 0;
  const validUrlCount = inputMode === 'single'
    ? (singleUrl.trim() ? 1 : 0)
    : parseUrls(textInput).length;

  // 单个模式下，成功/失败后显示简要结果而非进度列表
  const isSingleMode = inputMode === 'single' || total === 1;

  return (
    <Modal
      title="添加直播间"
      open={visible}
      onCancel={handleClose}
      width={inputMode === 'batch' ? 600 : 460}
      footer={
        processing ? null : (
          <Space>
            <Button onClick={handleClose}>
              {completed ? '关闭' : '取消'}
            </Button>
            {!completed && (
              <Button
                type="primary"
                onClick={handleStart}
                disabled={validUrlCount === 0}
              >
                {inputMode === 'single' ? '添加' : '开始添加'}
              </Button>
            )}
          </Space>
        )
      }
    >
      {/* 输入区域（仅未处理/未完成时显示） */}
      {!processing && !completed && (
        <>
          {/* 模式切换 */}
          <div style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 12 }}>
            <Radio.Group
              value={inputMode}
              onChange={e => {
                setInputMode(e.target.value);
                setImportMode('text');
              }}
              size="small"
            >
              <Radio.Button value="single">单个添加</Radio.Button>
              <Radio.Button value="batch">批量添加</Radio.Button>
            </Radio.Group>
            {inputMode === 'batch' && (
              <Radio.Group
                value={importMode}
                onChange={e => setImportMode(e.target.value)}
                size="small"
              >
                <Radio.Button value="text">手动输入</Radio.Button>
                <Radio.Button value="file">导入TXT</Radio.Button>
              </Radio.Group>
            )}
          </div>

          {/* 文件导入按钮 */}
          {inputMode === 'batch' && importMode === 'file' && (
            <Upload
              accept=".txt"
              showUploadList={false}
              beforeUpload={handleFileImport}
              style={{ marginBottom: 12 }}
            >
              <Button icon={<UploadOutlined />} size="small">选择 TXT 文件</Button>
            </Upload>
          )}

          {/* URL 输入 */}
          {inputMode === 'single' ? (
            <Input
              size="large"
              value={singleUrl}
              onChange={e => setSingleUrl(e.target.value)}
              placeholder="输入直播间地址，如 https://live.bilibili.com/12345"
              onPressEnter={handleStart}
            />
          ) : (
            <>
              <Alert
                type="info"
                showIcon
                message={`每行输入一个直播间地址，支持 # 开头的注释行，单次最多 ${MAX_BATCH_URLS} 个`}
                style={{ marginBottom: 8 }}
              />
              <TextArea
                rows={8}
                value={textInput}
                onChange={e => setTextInput(e.target.value)}
                placeholder={
                  'https://live.bilibili.com/12345\nhttps://www.douyu.com/67890\n# 这是注释行，会被忽略'
                }
                style={{ fontFamily: 'monospace', fontSize: 13 }}
              />
            </>
          )}

          {/* 立即监听开关 */}
          <div style={{ marginTop: 8 }}>
            <Switch
              checked={listen}
              onChange={(checked) => {
                setListen(checked);
                if (!checked) setNotifyOnly(false);
              }}
              size="small"
            />
            <Text style={{ marginLeft: 8 }}>添加后立即监听</Text>
          </div>

          {/* 仅开播提醒开关 */}
          <div style={{ marginTop: 8 }}>
            <Switch
              checked={notifyOnly}
              onChange={(checked) => {
                setNotifyOnly(checked);
                if (checked) setListen(true);
              }}
              size="small"
            />
            <Text style={{ marginLeft: 8 }}>仅开播提醒（不自动录制）</Text>
          </div>

          {/* 批量模式下的解析统计 */}
          {inputMode === 'batch' && validUrlCount > 0 && (
            <Text type="secondary" style={{ fontSize: 12, marginTop: 4, display: 'block' }}>
              已解析 {validUrlCount} 个链接
            </Text>
          )}
        </>
      )}

      {/* 进度/结果区域 */}
      {(processing || completed) && (
        <div>
          {/* 进度条 */}
          <div style={{ marginBottom: isSingleMode && completed ? 0 : 16 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
              <Text>
                {completed
                  ? isSingleMode
                    ? (failCount > 0 ? '添加失败' : '添加成功')
                    : `已完成 - 成功 ${successCount}，失败 ${failCount}`
                  : current >= 0
                    ? `已处理 ${current + 1}/${total}`
                    : `准备中...`}
              </Text>
              {!isSingleMode && <Text type="secondary">{progressPercent}%</Text>}
            </div>
            <Progress
              percent={progressPercent}
              status={completed ? (failCount > 0 ? 'exception' : 'success') : 'active'}
              showInfo={false}
            />
          </div>

          {/* 结果列表（批量模式或单个失败时显示） */}
          {(!isSingleMode || (isSingleMode && completed && failCount > 0)) && (
            <div style={{
              maxHeight: 300,
              overflow: 'auto',
              border: '1px solid #f0f0f0',
              borderRadius: 6,
              padding: 8,
            }}>
              {results.map((item, index) => (
                <div key={index} style={{ marginBottom: index < results.length - 1 ? 8 : 0 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    {item.status === 'pending' && (
                      <MinusCircleOutlined style={{ color: '#d9d9d9', fontSize: 14 }} />
                    )}
                    {item.status === 'success' && (
                      <CheckCircleOutlined style={{ color: '#52c41a', fontSize: 14 }} />
                    )}
                    {item.status === 'error' && (
                      <CloseCircleOutlined style={{ color: '#ff4d4f', fontSize: 14 }} />
                    )}
                    <Text
                      style={{
                        fontSize: 12,
                        color: item.status === 'error' ? '#ff4d4f' : undefined,
                        wordBreak: 'break-all',
                      }}
                    >
                      {item.url}
                    </Text>
                  </div>
                  {item.status === 'error' && item.error && (
                    <div style={{
                      marginLeft: 22,
                      marginTop: 2,
                      padding: '2px 8px',
                      background: '#fff2f0',
                      border: '1px solid #ffccc7',
                      borderRadius: 4,
                      fontSize: 12,
                      color: '#cf1322',
                    }}>
                      {item.error}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </Modal>
  );
};

export default AddRoomDialog;
