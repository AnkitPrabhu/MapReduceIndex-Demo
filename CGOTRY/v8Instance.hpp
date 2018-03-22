#ifndef v8Instance_hpp
#define v8Instance_hpp
#include<cstdio>
#include <stdio.h>
#include<map>
#include<stdlib.h>
#include<iostream>
#include<v8.h>
#include "Messages.h"
#include "Wrapper.h"

struct Data{
    msg_response * Rmsg; //It should not be a string
};

enum TYPE{
    STRING,
    INTNUMBER,
    FLOATNUMBER,
    BOOLEANTRUE,
    BOOLEANFALSE,
    ARRAYSTART,
    ARRAYEND,
    MAPSTART,
    MAPEND,
    UNDEFINED,
    JSONSTRING,
    EMITSTART,
    EMITEND,
};

class v8Instance{
    v8::Isolate *isolate_;
    v8::Persistent<v8::Context> context_;
    Data data;
    v8::Local<v8::Value> args[2];
    
    
public:
    v8Instance(v8::Platform *platform); //same
    ~v8Instance();
    v8::Isolate *GetIsolate() { return isolate_; }
    int v8WorkLoad(std::string source_path,const char* code);
    void Start();
    msg_response* Map(metaData value,const char* doc,std::string jsFile);
    
private:
    std::map<std::string,v8::Persistent<v8::Function>> on_map_;
    v8::Handle<v8::Object> ParseString(metaData meta);
    bool ExecuteScript(v8::Local<v8::String> source,v8::Local<v8::String> name);
};


#endif /* v8Instance_hpp */


