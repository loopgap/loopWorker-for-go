use std::slice;
use std::str;
use serde::{Serialize, Deserialize};

// 导入 Go Host Functions
extern "C" {
    fn host_http_request(
        url_ptr: *const u8,
        url_len: usize,
        body_ptr: *const u8,
        body_len: usize,
    ) -> u64;

    fn host_log(msg_ptr: *const u8, msg_len: usize);
}

// 内存分配器供 Host 写入返回值
#[no_mangle]
pub unsafe extern "C" fn alloc(size: usize) -> *mut u8 {
    let mut buf = Vec::with_capacity(size);
    let ptr = buf.as_mut_ptr();
    std::mem::forget(buf); // 阻止 Vec 在离开作用域时被析构释放
    ptr
}

// 释放内存以防泄露
#[no_mangle]
pub unsafe extern "C" fn dealloc(ptr: *mut u8, size: usize) {
    let _ = Vec::from_raw_parts(ptr, 0, size);
}

fn log(msg: &str) {
    unsafe {
        host_log(msg.as_ptr(), msg.len());
    }
}

// 演示向 host 发起 http 请求
fn request_http(url: &str, body: &str) -> Result<String, &'static str> {
    unsafe {
        let packed = host_http_request(
            url.as_ptr(),
            url.len(),
            body.as_ptr(),
            body.len(),
        );

        if packed == 0 {
            return Err("HTTP Request failed");
        }

        let res_ptr = (packed >> 32) as *mut u8;
        let res_len = (packed & 0xFFFFFFFF) as usize;

        // 重组字符串并读取
        let slice = slice::from_raw_parts(res_ptr, res_len);
        let res_str = str::from_utf8(slice).map_err(|_| "Invalid UTF-8")?.to_string();

        // 释放分配的内存
        dealloc(res_ptr, res_len);

        Ok(res_str)
    }
}

#[derive(Serialize, Deserialize)]
struct PromptRequest {
    prompt: String,
}

#[derive(Serialize, Deserialize)]
struct ModelResponse {
    result: String,
}

fn main() {
    log("Rust agent started execution.");
    
    let api_url = "http://api.openai.com/v1/chat/completions";
    let body = serde_json::to_string(&PromptRequest {
        prompt: "Hello, model!".to_string(),
    }).unwrap();

    log(&format!("Sending HTTP request to {}", api_url));
    match request_http(api_url, &body) {
        Ok(response) => {
            log(&format!("Received HTTP response: {}", response));
            // 处理结果并输出到标准输出供调度器捕获
            println!("{}", response);
        }
        Err(e) => {
            log(&format!("Error during request: {}", e));
        }
    }
}
