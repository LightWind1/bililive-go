import React, { useState, useMemo } from 'react';
import { Form, Input, Tag, Space, Tooltip } from 'antd';
import { FolderOutlined, FileOutlined } from '@ant-design/icons';

const { TextArea } = Input;

// 变量定义
export interface TemplateVariable {
  key: string;
  label: string;
  example: string;
  template: string;
}

// 预设模板定义
export interface PresetTemplate {
  name: string;
  description: string;
  template: string;
}

// 模拟数据（用于预览）
export interface MockStream {
  platform: string;
  platformCN: string;
  hostName: string;
  rooms: Array<{
    roomName: string;
    date: string;
    time: string;
  }>;
}

// 目录树节点
interface TreeNode {
  name: string;
  isFile: boolean;
  children?: TreeNode[];
}

// 构建目录树
const buildTree = (paths: string[]): TreeNode => {
  const root: TreeNode = { name: '/', isFile: false, children: [] };

  for (const path of paths) {
    const parts = path.split('/').filter(Boolean);
    let current = root;

    for (let i = 0; i < parts.length; i++) {
      const part = parts[i];
      const isFile = i === parts.length - 1;

      if (!current.children) {
        current.children = [];
      }

      let existing = current.children.find((c) => c.name === part);
      if (!existing) {
        existing = { name: part, isFile, children: isFile ? undefined : [] };
        current.children.push(existing);
      }

      if (!isFile) {
        current = existing;
      }
    }
  }

  // 排序：文件夹在前，文件在后
  const sortNodes = (node: TreeNode) => {
    if (node.children) {
      node.children.sort((a, b) => {
        if (a.isFile !== b.isFile) return a.isFile ? 1 : -1;
        return a.name.localeCompare(b.name);
      });
      node.children.forEach(sortNodes);
    }
  };
  sortNodes(root);

  return root;
};

// 渲染目录树节点
const TreeNodeComponent: React.FC<{ node: TreeNode; depth: number; maxDepth?: number }> = ({
  node,
  depth,
  maxDepth = 4,
}) => {
  if (depth > maxDepth) return null;

  const indent = depth * 20;
  const isFolder = !node.isFile;

  return (
    <div>
      <div
        style={{
          paddingLeft: indent,
          display: 'flex',
          alignItems: 'center',
          gap: 4,
          lineHeight: '22px',
          fontSize: 13,
        }}
      >
        {isFolder ? (
          <FolderOutlined style={{ color: '#faad14', fontSize: 14 }} />
        ) : (
          <FileOutlined style={{ color: '#1890ff', fontSize: 14 }} />
        )}
        <span style={{ color: isFolder ? '#333' : '#666' }}>{node.name}</span>
      </div>
      {isFolder && node.children && depth < maxDepth && (
        <div>
          {node.children.map((child, index) => (
            <TreeNodeComponent key={index} node={child} depth={depth + 1} maxDepth={maxDepth} />
          ))}
        </div>
      )}
    </div>
  );
};

// 目录树预览组件
const DirectoryTreePreview: React.FC<{ paths: string[]; streamCount: number }> = ({ paths, streamCount }) => {
  const tree = useMemo(() => buildTree(paths), [paths]);

  return (
    <div
      style={{
        marginTop: 12,
        padding: '12px 16px',
        background: '#fafafa',
        borderRadius: 6,
        border: '1px solid #e8e8e8',
        maxHeight: 300,
        overflow: 'auto',
      }}
    >
      <div style={{ fontSize: 12, color: '#666', marginBottom: 8 }}>
        📁 目录结构预览（含 {streamCount} 位主播的示例录制）
      </div>
      <TreeNodeComponent node={tree} depth={0} maxDepth={4} />
    </div>
  );
};

// 检查变量是否应该被禁用（互斥规则）
const isVariableDisabled = (variable: TemplateVariable, currentTemplate: string): { disabled: boolean; reason?: string } => {
  if (!currentTemplate) return { disabled: false };

  // 互斥规则：FileName 和 Ext 不能同时使用
  if (variable.key === 'FileName') {
    if (currentTemplate.includes('{{ .Ext }}') || currentTemplate.includes('{{.Ext}}')) {
      return { disabled: true, reason: '已使用「扩展名」，文件名已包含扩展名' };
    }
  }
  if (variable.key === 'Ext') {
    if (currentTemplate.includes('{{ .FileName }}') || currentTemplate.includes('{{.FileName}}')) {
      return { disabled: true, reason: '已使用「文件名」，文件名已包含扩展名' };
    }
  }

  // 互斥规则：Date 和 DateTime 不能同时使用
  if (variable.key === 'Date') {
    if (currentTemplate.includes('2006-01-02 15-04-05')) {
      return { disabled: true, reason: '已使用「日期时间」，已包含日期' };
    }
  }
  if (variable.key === 'DateTime') {
    if (currentTemplate.includes('{{ now | date "2006-01-02" }}') || currentTemplate.includes('{{now | date "2006-01-02"}}')) {
      return { disabled: true, reason: '已使用「日期」，请改用「日期时间」' };
    }
  }

  return { disabled: false };
};

// 组件属性
interface TemplateBuilderProps {
  /** 表单字段路径，如 ['on_record_finished', 'cloud_upload', 'upload_path_tmpl'] */
  name: string[];
  /** 可用变量列表 */
  variables: TemplateVariable[];
  /** 预设模板列表 */
  presets: PresetTemplate[];
  /** 模拟数据 */
  mockData: MockStream[];
  /** 模板渲染函数：将模板字符串转换为实际路径 */
  renderTemplate: (template: string, stream: MockStream, room: MockStream['rooms'][0]) => string;
  /** 输入框占位符 */
  placeholder?: string;
  /** 是否显示目录树预览 */
  showTreePreview?: boolean;
  /** 输入框宽度 */
  width?: number;
}

/**
 * 通用模板构建器组件
 * 支持预设模板选择、变量插入、实时预览、目录树展示
 */
const TemplateBuilder: React.FC<TemplateBuilderProps> = ({
  name,
  variables,
  presets,
  mockData,
  renderTemplate,
  placeholder = '',
  showTreePreview = true,
  width = 500,
}) => {
  const form = Form.useFormInstance();
  const [customMode, setCustomMode] = useState(false);

  // 获取当前模板值
  const currentTemplate = Form.useWatch(name, form) || '';

  // 检查是否匹配预设模板
  const matchedPreset = presets.find((t) => t.template === currentTemplate);

  // 选择预设模板
  const handleSelectPreset = (template: string) => {
    setCustomMode(false);
    const fieldValue: any = {};
    let current = fieldValue;
    for (let i = 0; i < name.length - 1; i++) {
      current[name[i]] = {};
      current = current[name[i]];
    }
    current[name[name.length - 1]] = template;
    form.setFieldsValue(fieldValue);
  };

  // 插入变量
  const handleInsertVariable = (variable: TemplateVariable) => {
    const textarea = document.querySelector(`textarea[name="${name.join('-')}"]`) as HTMLTextAreaElement;

    // 常用分隔符
    const separators = ['/', '-', '.', '_', ' ', '|'];

    // 智能添加分隔符
    const getSeparator = (before: string, after: string): string => {
      if (!before && !after) return '/';
      if (!before) return '';
      if (separators.includes(before.slice(-1))) return '';
      if (before.endsWith('"')) return '/';
      if (before.endsWith('}')) return '/';
      return '/';
    };

    if (textarea) {
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      const before = currentTemplate.substring(0, start);
      const after = currentTemplate.substring(end);
      const separator = getSeparator(before, after);
      const newTemplate = before + separator + variable.template + after;

      const fieldValue: any = {};
      let current = fieldValue;
      for (let i = 0; i < name.length - 1; i++) {
        current[name[i]] = {};
        current = current[name[i]];
      }
      current[name[name.length - 1]] = newTemplate;
      form.setFieldsValue(fieldValue);

      setTimeout(() => {
        textarea.focus();
        const newPos = start + separator.length + variable.template.length;
        textarea.setSelectionRange(newPos, newPos);
      }, 0);
    } else {
      const separator = getSeparator(currentTemplate, '');
      const newTemplate = currentTemplate + separator + variable.template;

      const fieldValue: any = {};
      let current = fieldValue;
      for (let i = 0; i < name.length - 1; i++) {
        current[name[i]] = {};
        current = current[name[i]];
      }
      current[name[name.length - 1]] = newTemplate;
      form.setFieldsValue(fieldValue);
    }
  };

  // 生成预览路径
  const previewPaths = useMemo(() => {
    if (!currentTemplate || !showTreePreview) return [];

    const paths: string[] = [];
    for (const stream of mockData) {
      for (const room of stream.rooms) {
        const path = renderTemplate(currentTemplate, stream, room);
        paths.push(path);
      }
    }
    return paths;
  }, [currentTemplate, mockData, renderTemplate, showTreePreview]);

  return (
    <div>
      {/* 预设模板选择 */}
      <div style={{ marginBottom: 12 }}>
        <div style={{ marginBottom: 8, fontSize: 12, color: '#666' }}>选择预设模板：</div>
        <Space wrap>
          {presets.map((preset) => (
            <Tag
              key={preset.name}
              color={!customMode && matchedPreset?.name === preset.name ? 'blue' : 'default'}
              style={{ cursor: 'pointer', padding: '4px 12px' }}
              onClick={() => handleSelectPreset(preset.template)}
            >
              {preset.name}
            </Tag>
          ))}
          <Tag
            color={customMode ? 'blue' : 'default'}
            style={{ cursor: 'pointer', padding: '4px 12px' }}
            onClick={() => setCustomMode(true)}
          >
            自定义
          </Tag>
        </Space>
      </div>

      {/* 变量插入按钮 */}
      {(customMode || !matchedPreset) && (
        <div style={{ marginBottom: 12 }}>
          <div style={{ marginBottom: 8, fontSize: 12, color: '#666' }}>点击插入变量：</div>
          <Space wrap>
            {variables.map((variable) => {
              const { disabled, reason } = isVariableDisabled(variable, currentTemplate);
              return (
                <Tooltip key={variable.key} title={disabled ? reason : `示例: ${variable.example}`}>
                  <Tag
                    color={disabled ? 'default' : 'green'}
                    style={{
                      cursor: disabled ? 'not-allowed' : 'pointer',
                      padding: '4px 12px',
                      opacity: disabled ? 0.5 : 1,
                    }}
                    onClick={() => !disabled && handleInsertVariable(variable)}
                  >
                    {variable.label}
                  </Tag>
                </Tooltip>
              );
            })}
          </Space>
        </div>
      )}

      {/* 模板输入框 */}
      <Form.Item name={name} noStyle>
        <TextArea
          name={name.join('-')}
          rows={2}
          placeholder={placeholder}
          style={{ width, fontFamily: 'monospace' }}
          onFocus={() => {
            if (!matchedPreset) setCustomMode(true);
          }}
        />
      </Form.Item>

      {/* 目录树预览 */}
      {showTreePreview && previewPaths.length > 0 && (
        <DirectoryTreePreview paths={previewPaths} streamCount={mockData.length} />
      )}
    </div>
  );
};

export default TemplateBuilder;
